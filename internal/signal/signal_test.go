package signal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
)

type testConnection struct {
	client *Client
	done   chan struct{}
}

func signalTestServer(t *testing.T, hub *Hub, accounts []int64, ips []string) (string, <-chan testConnection) {
	t.Helper()
	accepted := make(chan testConnection, len(accounts))
	var index atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		i := int(index.Add(1) - 1)
		client := newClient(conn, hub, accounts[i], "player", "", "ROOM", ips[i], zerolog.Nop())
		hub.register(client)
		done := make(chan struct{})
		accepted <- testConnection{client: client, done: done}
		go client.writePump()
		client.readPump()
		close(done)
	}))
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http"), accepted
}

func dialSignal(t *testing.T, url string, accepted <-chan testConnection) (*websocket.Conn, testConnection) {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	select {
	case registered := <-accepted:
		t.Cleanup(func() {
			registered.client.close()
			waitSignalDone(t, registered.done)
		})
		return conn, registered
	case <-time.After(3 * time.Second):
		t.Fatal("connection was not registered")
		return nil, testConnection{}
	}
}

func waitSignalDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("readPump did not exit")
	}
}

func readSignal(t *testing.T, conn *websocket.Conn) Envelope {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var env Envelope
	if err := conn.ReadJSON(&env); err != nil {
		t.Fatalf("read message: %v", err)
	}
	return env
}

func TestStartGameBroadcastsRelayRegardlessOfRemoteIP(t *testing.T) {
	for _, tc := range []struct {
		name string
		ips  []string
	}{
		{"loopback", []string{"loopback", "loopback"}},
		{"same public IP", []string{"203.0.113.1", "203.0.113.1"}},
		{"different public IPs", []string{"203.0.113.1", "198.51.100.1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub := NewHub(zerolog.Nop())
			hub.SetHostResolver(func(string) int64 { return 1 })
			url, accepted := signalTestServer(t, hub, []int64{1, 2}, tc.ips)
			host, _ := dialSignal(t, url, accepted)
			peer, _ := dialSignal(t, url, accepted)
			if err := host.WriteJSON(Envelope{Type: TypeStartGame}); err != nil {
				t.Fatal(err)
			}
			for _, conn := range []*websocket.Conn{host, peer} {
				env := readSignal(t, conn)
				if env.Type != TypeGameStarting {
					t.Fatalf("message type = %q, want game_starting", env.Type)
				}
				var payload GameStartingPayload
				if err := json.Unmarshal(env.Payload, &payload); err != nil {
					t.Fatal(err)
				}
				if payload.TransportMode != "relay" || payload.HostAccountID != 1 {
					t.Errorf("game_starting = %+v, want host 1 and relay", payload)
				}
			}
		})
	}
}

func TestReplacedConnectionDoesNotDisconnectOnlineMember(t *testing.T) {
	hub := NewHub(zerolog.Nop())
	var disconnects atomic.Int32
	var memberOnline atomic.Bool
	memberOnline.Store(true)
	hub.SetDisconnectHandler(func(_ context.Context, roomCode string, accountID int64) {
		if roomCode != "ROOM" || accountID != 1 {
			t.Errorf("unexpected disconnect: %s/%d", roomCode, accountID)
		}
		memberOnline.Store(false)
		disconnects.Add(1)
	})
	url, accepted := signalTestServer(t, hub, []int64{1, 1}, []string{"loopback", "loopback"})
	old, oldRegistered := dialSignal(t, url, accepted)
	replacement, registered := dialSignal(t, url, accepted)
	waitSignalDone(t, oldRegistered.done)
	if err := old.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := old.ReadMessage(); err == nil {
		t.Error("replaced WebSocket is still open")
	}
	if !memberOnline.Load() || disconnects.Load() != 0 {
		t.Errorf("old readPump removed online member: online=%v disconnects=%d", memberOnline.Load(), disconnects.Load())
	}
	clients := hub.ClientsIn("ROOM")
	if len(clients) != 1 || clients[0] != registered.client {
		t.Fatalf("replacement is not the sole registered connection: %v", clients)
	}
	if err := replacement.WriteJSON(Envelope{Type: TypePing, Seq: 42}); err != nil {
		t.Fatal(err)
	}
	if env := readSignal(t, replacement); env.Type != TypePong || env.Seq != 42 {
		t.Errorf("replacement response = %+v, want pong seq 42", env)
	}
	if !hub.SendTo("ROOM", 1, Envelope{Type: TypeRoomState}) {
		t.Fatal("cannot send to replacement")
	}
	if env := readSignal(t, replacement); env.Type != TypeRoomState {
		t.Errorf("replacement delivery = %q, want room_state", env.Type)
	}
	_ = replacement.Close()
	waitSignalDone(t, registered.done)
	if memberOnline.Load() || disconnects.Load() != 1 {
		t.Errorf("current disconnect: online=%v disconnects=%d, want false/1", memberOnline.Load(), disconnects.Load())
	}
	if len(hub.ClientsIn("ROOM")) != 0 {
		t.Error("disconnected replacement remains registered")
	}
}
