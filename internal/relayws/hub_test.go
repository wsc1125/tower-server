package relayws

import (
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
)

func testConn(t *testing.T, h *Hub, id int64) *Conn {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		accepted <- ws
	}))
	t.Cleanup(server.Close)
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &Conn{ws: <-accepted, send: make(chan outbound, sendBuffer), hub: h, accountID: id, closed: make(chan struct{})}
	t.Cleanup(func() { c.close(); _ = client.Close() })
	return c
}

func testFrame(id int64, payload byte) []byte {
	frame := make([]byte, headerSize+1)
	frame[0] = dataMessageType
	binary.BigEndian.PutUint64(frame[1:headerSize], uint64(id))
	frame[headerSize] = payload
	return frame
}

func expectControl(t *testing.T, c *Conn, kind string, id int64) {
	t.Helper()
	select {
	case o := <-c.send:
		var msg ControlMessage
		if o.msgType != websocket.TextMessage || json.Unmarshal(o.data, &msg) != nil || msg.Type != kind || msg.ClientID != id {
			t.Fatalf("want %s(%d), got type=%d data=%q", kind, id, o.msgType, o.data)
		}
	default:
		t.Fatalf("missing %s(%d)", kind, id)
	}
}

func expectData(t *testing.T, c *Conn, id int64, payload byte) {
	t.Helper()
	select {
	case o := <-c.send:
		if o.msgType != websocket.BinaryMessage || len(o.data) != headerSize+1 || int64(binary.BigEndian.Uint64(o.data[1:headerSize])) != id || o.data[headerSize] != payload {
			t.Fatalf("unexpected data: type=%d data=%v", o.msgType, o.data)
		}
	default:
		t.Fatal("missing data")
	}
}

func expectOpen(t *testing.T, c *Conn) {
	t.Helper()
	select {
	case <-c.closed:
		t.Fatal("connection unexpectedly closed")
	default:
	}
}

func TestPeerJoinedBeforeData(t *testing.T) {
	for _, hostFirst := range []bool{true, false} {
		name := "member_first"
		if hostFirst {
			name = "host_first"
		}
		t.Run(name, func(t *testing.T) {
			h := NewHub(zerolog.Nop())
			host, member := testConn(t, h, 1), testConn(t, h, 42)
			if hostFirst {
				h.Join("ROOM", host, true)
			}
			h.Join("ROOM", member, false)
			h.Route(member, testFrame(0, 1))
			if !hostFirst {
				h.Join("ROOM", host, true)
			}
			h.Route(member, testFrame(0, 2))
			expectControl(t, host, "peer_joined", 42)
			expectData(t, host, 42, 1)
			expectData(t, host, 42, 2)
		})
	}
}

func TestHostJoinAnnouncesAllMembersBeforeConcurrentData(t *testing.T) {
	h := NewHub(zerolog.Nop())
	host := testConn(t, h, 1)
	members := []*Conn{testConn(t, h, 42), testConn(t, h, 43)}
	for _, c := range members {
		h.Join("ROOM", c, false)
		h.Route(c, testFrame(0, 1))
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); h.Join("ROOM", host, true) }()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			for _, c := range members {
				h.Route(c, testFrame(0, 2))
			}
		}
	}()
	wg.Wait()
	joined := make(map[int64]bool)
	frames := 0
	for len(host.send) > 0 {
		o := <-host.send
		if o.msgType == websocket.TextMessage {
			var msg ControlMessage
			if err := json.Unmarshal(o.data, &msg); err != nil {
				t.Fatal(err)
			}
			if msg.Type != "peer_joined" || joined[msg.ClientID] {
				t.Fatalf("unexpected control: %+v", msg)
			}
			joined[msg.ClientID] = true
		} else {
			id := int64(binary.BigEndian.Uint64(o.data[1:headerSize]))
			if !joined[id] {
				t.Fatalf("data from %d preceded peer_joined", id)
			}
			frames++
		}
	}
	if len(joined) != 2 || frames != 102 {
		t.Fatalf("joined=%v frames=%d", joined, frames)
	}
}

func TestReplacedHostLeaveKeepsNewHostAndMembers(t *testing.T) {
	h := NewHub(zerolog.Nop())
	oldHost, newHost, member := testConn(t, h, 1), testConn(t, h, 1), testConn(t, h, 42)
	h.Join("ROOM", oldHost, true)
	h.Join("ROOM", member, false)
	h.Join("ROOM", newHost, true)
	h.Leave(oldHost)
	if h.rooms["ROOM"] == nil || h.rooms["ROOM"].host != newHost {
		t.Fatal("old host removed replacement room")
	}
	expectOpen(t, newHost)
	expectOpen(t, member)
	expectControl(t, newHost, "peer_joined", 42)
	h.Route(member, testFrame(0, 1))
	expectData(t, newHost, 42, 1)
	h.Leave(newHost)
	if h.rooms["ROOM"] != nil {
		t.Fatal("current host leave must remove room")
	}
	select {
	case <-member.closed:
	default:
		t.Fatal("current host leave must close members")
	}
}

func TestResumeForwardedToHostOnly(t *testing.T) {
	h := NewHub(zerolog.Nop())
	host, member := testConn(t, h, 1), testConn(t, h, 42)
	h.Join("ROOM", host, true)
	h.Join("ROOM", member, false)
	expectControl(t, host, "peer_joined", 42)

	// 成员发 resume → 房主收到（带成员账号 ID）。
	h.Resume(member)
	expectControl(t, host, "resume", 42)

	// 房主发 resume → 不转发（resume 只对成员有意义）。
	h.Resume(host)
	if len(host.send) != 0 {
		t.Fatal("host resume must not be forwarded")
	}

	// resume 不影响数据转发。
	h.Route(member, testFrame(0, 1))
	expectData(t, host, 42, 1)
}

func TestMemberReplacementDeduplicatesEventsAndRejectsStaleData(t *testing.T) {
	h := NewHub(zerolog.Nop())
	host, oldMember, newMember := testConn(t, h, 1), testConn(t, h, 42), testConn(t, h, 42)
	h.Join("ROOM", host, true)
	h.Join("ROOM", oldMember, false)
	expectControl(t, host, "peer_joined", 42)
	h.Join("ROOM", newMember, false)
	h.Leave(oldMember)
	h.Route(oldMember, testFrame(0, 9))
	if len(host.send) != 0 {
		t.Fatal("replacement emitted duplicate join, stale left, or stale data")
	}
	h.Route(newMember, testFrame(0, 1))
	expectData(t, host, 42, 1)
	h.Leave(newMember)
	expectControl(t, host, "peer_left", 42)
	h.Leave(newMember)
	if len(host.send) != 0 {
		t.Fatal("duplicate leave emitted peer_left")
	}
	h.Join("ROOM", testConn(t, h, 42), false)
	expectControl(t, host, "peer_joined", 42)
}

func TestDepartedPendingMemberIsNotReplayed(t *testing.T) {
	h := NewHub(zerolog.Nop())
	member, host := testConn(t, h, 42), testConn(t, h, 1)
	h.Join("ROOM", member, false)
	// Keep the room alive when the pending member leaves.
	h.Join("ROOM", testConn(t, h, 43), false)
	h.Route(member, testFrame(0, 1))
	h.Leave(member)
	h.Join("ROOM", host, true)
	expectControl(t, host, "peer_joined", 43)
	if len(host.send) != 0 {
		t.Fatal("departed member data was replayed")
	}
}
