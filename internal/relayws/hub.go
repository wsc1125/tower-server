// Package relayws 战斗流量中继：转发 NGO 的 WebSocket 中继传输数据。
//
// 与信令（signal 包）的职责边界：信令只交换连接信息，本模块承载战斗数据本身。
// 这是路线 B（中继转发）的精简实现：所有流量走服务端 TCP（WebSocket），
// 天然穿越一切 NAT/防火墙 —— 不做打洞，用服务器带宽换 100% 的连通率。
// 打洞优化（WebRTC + coturn）留作后续路线 A（见《联机方案.md》路线对比）。
//
// 线路协议：
//   - 二进制帧 = 战斗数据：[0x01][8 字节大端 ID][payload]。
//     帧头在整条链路上始终保留（客户端 HandleData 依赖它解析发送者）：
//     成员 → 房主：成员发的 ID 恒为 0（服务端 ID），服务端改写为发送者账号 ID 后转给房主；
//     房主 → 成员：ID 为目标账号 ID，服务端据此路由，帧头原样转发。
//   - 文本帧 = 控制消息：welcome（分配 ID）/ peer_joined（成员加入）/ peer_left（成员断线）/ kick（房主踢人）。
//     每个房主连接先收到成员的 peer_joined，再收到该成员的任何 Data；同账号替换不重复通知当前房主。
//
// NGO 侧的传输实现见客户端 WebSocketRelayTransport。
package relayws

import (
	"encoding/binary"
	"encoding/json"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
)

// 连接参数（与 signal 包保持同一量级；读上限放宽 —— 战斗帧更大）。
const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = 30 * time.Second

	// maxMessageSize 单条 WebSocket 消息上限。NGO 分片消息最大 64KB 级，留足余量。
	maxMessageSize = 1024 * 1024

	// sendBuffer 每个连接的发送队列长度。战斗帧频率高，比信令给得宽。
	sendBuffer = 256

	// dataMessageType 战斗数据帧的标记字节。
	dataMessageType byte = 0x01

	// headerSize 帧头大小：1 字节类型 + 8 字节大端 ID。
	headerSize = 9

	// maxPendingFrames 房主未就绪时缓冲的帧数上限，超出丢弃最早的。
	// 房主从中继注册通常只比成员慢几十毫秒，正常远用不满。
	maxPendingFrames = 256
)

// 控制消息类型（文本帧 JSON 的 type 字段）。
const (
	controlWelcome    = "welcome"
	controlPeerJoined = "peer_joined"
	controlPeerLeft   = "peer_left"
	controlKick       = "kick"
)

// ControlMessage 控制帧。
type ControlMessage struct {
	Type     string `json:"type"`
	ClientID int64  `json:"client_id"`
}

// outbound 一条待发送消息。
type outbound struct {
	msgType int
	data    []byte
}

// Conn 一条中继连接。
type Conn struct {
	ws        *websocket.Conn
	send      chan outbound
	hub       *Hub
	room      *Room
	accountID int64
	isHost    bool

	closeOnce sync.Once
	closed    chan struct{}

	kickMu sync.Mutex
	kicked bool
}

// markKicked 标记为被踢：关闭时不再向房主发 peer_left（房主已主动移除该成员）。
func (c *Conn) markKicked() {
	c.kickMu.Lock()
	c.kicked = true
	c.kickMu.Unlock()
}

func (c *Conn) isKicked() bool {
	c.kickMu.Lock()
	defer c.kickMu.Unlock()

	return c.kicked
}

// trySend 尝试投递。队列满返回 false，调用方应断开该连接。
func (c *Conn) trySend(o outbound) bool {
	select {
	case c.send <- o:
		return true
	case <-c.closed:
		return false
	default:
		return false
	}
}

// close 关闭连接。幂等。
func (c *Conn) close() {
	c.closeOnce.Do(func() {
		close(c.closed)
		_ = c.ws.Close()
	})
}

// Room 一个房间的中继拓扑：一个房主 + 若干成员。
type Room struct {
	code string

	// host 房主连接。房主是 NGO 的会话中心，离开即房间解散。
	host *Conn

	// clients 成员连接。账号 ID 即 NGO 侧的传输层 clientId。
	clients map[int64]*Conn

	// pending 房主尚未注册时缓冲的「成员 → 房主」帧。
	// 成员先于房主连入中继是常态（房主还要先走完 NGO 启动流程），
	// 不缓冲的话成员的首批握手帧会被丢弃，NGO 握手失败。
	pending [][]byte
}

// Hub 中继连接管理中心。
type Hub struct {
	mu    sync.Mutex
	rooms map[string]*Room

	logger zerolog.Logger
}

// NewHub 创建中继管理中心。
func NewHub(logger zerolog.Logger) *Hub {
	return &Hub{
		rooms:  make(map[string]*Room),
		logger: logger,
	}
}

// Join 登记连接。房主占据 host 槽位（重复连接顶掉旧连接），成员按账号占槽。
// 房主就绪时，把缓冲的成员帧按序冲给它。
func (h *Hub) Join(roomCode string, c *Conn, isHost bool) {
	h.mu.Lock()

	room, ok := h.rooms[roomCode]
	if !ok {
		room = &Room{code: roomCode, clients: make(map[int64]*Conn)}
		h.rooms[roomCode] = room
	}
	c.room = room
	c.isHost = isHost

	var replaced *Conn
	if isHost {
		replaced = room.host
		room.host = c
	} else {
		replaced = room.clients[c.accountID]
		room.clients[c.accountID] = c
	}

	// 加入通知和数据都在同一临界区内入队，确保任何 Data 前已有 peer_joined。
	var pending [][]byte
	if isHost {
		for id := range room.clients {
			c.enqueueControl(controlPeerJoined, id)
		}
		pending = room.pending
		room.pending = nil
		for _, frame := range pending {
			if !c.trySend(outbound{msgType: websocket.BinaryMessage, data: frame}) {
				c.close()
				break
			}
		}
	} else if replaced == nil && room.host != nil {
		room.host.enqueueControl(controlPeerJoined, c.accountID)
	}

	h.mu.Unlock()

	if replaced != nil {
		replaced.markKicked()
		replaced.close()
	}

	h.logger.Info().
		Str("room_code", roomCode).
		Int64("account_id", c.accountID).
		Bool("is_host", isHost).
		Int("pending_flushed", len(pending)).
		Msg("中继连接已登记")
}

// Leave 注销连接。房主离开即关闭房间全部连接；成员离开通知房主。
func (h *Hub) Leave(c *Conn) {
	h.mu.Lock()

	room := c.room
	if room == nil {
		h.mu.Unlock()
		return
	}

	if h.rooms[room.code] != room {
		h.mu.Unlock()
		return
	}

	if c.isHost {
		if room.host != c {
			h.mu.Unlock()
			return
		}
		// 摘除当前房间后再关闭连接，避免按房间码误关新建或替换后的房间。
		delete(h.rooms, room.code)
		h.mu.Unlock()
		c.close()
		for _, member := range room.clients {
			member.close()
		}
		h.logger.Info().Str("room_code", room.code).Msg("房主已断开中继连接，中继房间已关闭")
		return
	}

	if room.clients[c.accountID] != c {
		h.mu.Unlock()
		return
	}
	delete(room.clients, c.accountID)
	pending := room.pending[:0]
	for _, frame := range room.pending {
		if int64(binary.BigEndian.Uint64(frame[1:headerSize])) != c.accountID {
			pending = append(pending, frame)
		}
	}
	room.pending = pending

	if room.host == nil && len(room.clients) == 0 {
		delete(h.rooms, room.code)
	}
	if room.host != nil && !c.isKicked() {
		room.host.enqueueControl(controlPeerLeft, c.accountID)
	}
	h.mu.Unlock()
}

// closeRoom 关闭房间内所有连接。
func (h *Hub) closeRoom(roomCode, reason string) {
	h.mu.Lock()
	room, ok := h.rooms[roomCode]
	if !ok {
		h.mu.Unlock()
		return
	}
	delete(h.rooms, roomCode)
	h.mu.Unlock()

	if room.host != nil {
		room.host.markKicked() // 房主自己触发的关闭，无需再走 Leave 逻辑
		room.host.close()
	}

	for _, c := range room.clients {
		c.close()
	}

	h.logger.Info().Str("room_code", roomCode).Str("reason", reason).Msg("中继房间已关闭")
}

// Route 转发一条战斗数据帧。
//
// 房主 → 成员：按帧内 ID 路由；成员 → 房主：改写帧头为发送者账号 ID 后转发。
func (h *Hub) Route(sender *Conn, frame []byte) {
	if len(frame) < headerSize || frame[0] != dataMessageType {
		h.logger.Warn().
			Str("room_code", sender.roomCode()).
			Int64("account_id", sender.accountID).
			Msg("非法中继数据帧，断开该连接")
		sender.close()
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	room := sender.room
	if room == nil || h.rooms[room.code] != room {
		return
	}
	if sender.isHost && room.host != sender || !sender.isHost && room.clients[sender.accountID] != sender {
		return
	}

	if sender.isHost {
		// 房主 → 成员：帧头 ID 是目标账号 ID，据此路由。
		targetID := int64(binary.BigEndian.Uint64(frame[1:headerSize]))
		target := room.clients[targetID]

		// 目标已断开：静默丢弃。NGO 房主侧会经 peer_left 感知断线并移除玩家。
		if target == nil {
			return
		}

		// 帧头原样保留：客户端 HandleData 要求每个二进制帧都带帧头，
		// 成员侧据此把 ID 统一映射为 ServerClientId。
		if !target.trySend(outbound{msgType: websocket.BinaryMessage, data: frame}) {
			target.close()
		}
		return
	}

	// 成员 → 房主：帧头 ID 改写为发送者账号 ID，房主侧据此还原 NGO clientId。
	// 帧头必须保留 —— 客户端 HandleData 依赖它解析发送者。
	frameToHost := make([]byte, len(frame))
	copy(frameToHost, frame)
	binary.BigEndian.PutUint64(frameToHost[1:headerSize], uint64(sender.accountID))

	if room.host == nil {
		if len(room.pending) >= maxPendingFrames {
			room.pending = room.pending[1:]
		}
		room.pending = append(room.pending, frameToHost)
		return
	}

	host := room.host

	if !host.trySend(outbound{msgType: websocket.BinaryMessage, data: frameToHost}) {
		host.close()
	}
}

// roomCode 连接所在房间码（日志用）。
func (c *Conn) roomCode() string {
	if c.room == nil {
		return ""
	}

	return c.room.code
}

// readPump 读循环：接收并分发消息。
func (c *Conn) readPump() {
	defer func() {
		c.hub.Leave(c)
		c.close()
	}()

	c.ws.SetReadLimit(maxMessageSize)
	_ = c.ws.SetReadDeadline(time.Now().Add(pongWait))
	c.ws.SetPongHandler(func(string) error {
		return c.ws.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		messageType, raw, err := c.ws.ReadMessage()
		if err != nil {
			return
		}

		// 任何消息都算活动，刷新读超时。
		_ = c.ws.SetReadDeadline(time.Now().Add(pongWait))

		switch messageType {
		case websocket.BinaryMessage:
			c.hub.Route(c, raw)

		case websocket.TextMessage:
			var msg ControlMessage
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
			}

			if msg.Type == controlKick && c.isHost {
				h := c.hub
				h.mu.Lock()
				var targetConn *Conn
				if c.room != nil {
					targetConn = c.room.clients[msg.ClientID]
				}
				h.mu.Unlock()

				if targetConn != nil {
					targetConn.markKicked()
					targetConn.close()
				}
			}
		}
	}
}

// writePump 写循环：把发送队列里的消息写到连接上，并定期 ping 保活。
func (c *Conn) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.close()
	}()

	for {
		select {
		case o, ok := <-c.send:
			if !ok {
				return
			}

			_ = c.ws.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.ws.WriteMessage(o.msgType, o.data); err != nil {
				return
			}

		case <-ticker.C:
			_ = c.ws.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.ws.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}

		case <-c.closed:
			return
		}
	}
}

// enqueueWelcome 下发 welcome 控制帧，告知 NGO 侧本连接的传输层 ID。
func (c *Conn) enqueueWelcome(clientID int64) {
	c.enqueueControl(controlWelcome, clientID)
}

func (c *Conn) enqueueControl(kind string, clientID int64) {
	payload, err := json.Marshal(ControlMessage{Type: kind, ClientID: clientID})
	if err != nil {
		return
	}

	if !c.trySend(outbound{msgType: websocket.TextMessage, data: payload}) {
		c.close()
	}
}
