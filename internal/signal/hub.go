package signal

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
)

// 连接参数。
const (
	// writeWait 单条消息的写超时。
	writeWait = 10 * time.Second

	// pongWait 读超时：超过此时长未收到任何消息（含 pong）则断开。
	pongWait = 60 * time.Second

	// pingPeriod 服务端主动 ping 的间隔。必须小于 pongWait，
	// 否则服务端会在客户端还活着时先判定超时。
	pingPeriod = 30 * time.Second

	// maxMessageSize 单条 WebSocket 消息上限。
	// 比 maxSignalDataSize 略大，留出信封与 JSON 转义的余量。
	maxMessageSize = 80 * 1024

	// sendBuffer 每个连接的发送队列长度。
	//
	// 队列满说明客户端消费不过来（网络慢或卡死），此时直接断开 ——
	// 阻塞等待会让一个慢客户端拖垮整个房间的广播。
	sendBuffer = 64

	// messageRateLimit 单连接每秒消息数上限。
	messageRateLimit = 30
)

// Client 一条 WebSocket 连接。
type Client struct {
	accountID   int64
	displayName string
	roomCode    string

	conn *websocket.Conn
	hub  *Hub
	log  zerolog.Logger

	// send 发送队列。所有写操作都经由此 channel，
	// 保证同一连接上不会有两个 goroutine 并发写（gorilla/websocket 不允许）。
	send chan Envelope

	// ready 准备状态。由信令消息更新，房间状态广播时读取。
	readyMu sync.RWMutex
	ready   bool

	closeOnce sync.Once
	closed    chan struct{}
}

// AccountID 账号 ID。
func (c *Client) AccountID() int64 { return c.accountID }

// DisplayName 昵称。
func (c *Client) DisplayName() string { return c.displayName }

// RoomCode 所在房间码。
func (c *Client) RoomCode() string { return c.roomCode }

// IsReady 是否已准备。
func (c *Client) IsReady() bool {
	c.readyMu.RLock()
	defer c.readyMu.RUnlock()

	return c.ready
}

// setReady 更新准备状态。
func (c *Client) setReady(ready bool) {
	c.readyMu.Lock()
	defer c.readyMu.Unlock()

	c.ready = ready
}

// trySend 尝试投递消息。队列满时返回 false（调用方据此断开该连接）。
func (c *Client) trySend(env Envelope) bool {
	select {
	case c.send <- env:
		return true
	case <-c.closed:
		return false
	default:
		// 队列满：客户端消费不过来。
		return false
	}
}

// close 关闭连接。幂等。
func (c *Client) close() {
	c.closeOnce.Do(func() {
		close(c.closed)
		_ = c.conn.Close()
	})
}

// Hub 信令连接管理中心。
//
// 按房间码分组管理连接。房间的成员列表以 room 模块为准，
// Hub 只维护「账号 → 连接」的映射，用于消息投递。
type Hub struct {
	mu sync.RWMutex

	// rooms 房间码 → (账号 ID → 连接)。
	rooms map[string]map[int64]*Client

	logger zerolog.Logger

	// onDisconnect 连接断开时的回调，由 room 模块注入（触发房主迁移）。
	onDisconnect func(ctx context.Context, roomCode string, accountID int64)

	// hostResolver 查询房间当前房主，由 room 模块注入。
	//
	// 用注入而非直接依赖 room 包：Hub 只关心连接，
	// 「谁是房主」是 room 模块的职责。
	hostResolver func(roomCode string) int64
}

// NewHub 创建连接管理中心。
func NewHub(logger zerolog.Logger) *Hub {
	return &Hub{
		rooms:  make(map[string]map[int64]*Client),
		logger: logger,
	}
}

// SetDisconnectHandler 注入断线回调。
//
// 用注入而非直接依赖 room 包：Hub 只关心连接，
// 「断线后房间怎么处理」是 room 模块的职责。
func (h *Hub) SetDisconnectHandler(fn func(ctx context.Context, roomCode string, accountID int64)) {
	h.onDisconnect = fn
}

// SetHostResolver 注入房主查询函数。
func (h *Hub) SetHostResolver(fn func(roomCode string) int64) {
	h.hostResolver = fn
}

// register 登记连接。同一账号重复连接时，旧连接被顶掉。
//
// 顶号而非拒绝：客户端异常退出后可能残留旧连接，
// 若拒绝新连接，用户会陷入「连不上又等不到超时」的困境。
func (h *Hub) register(c *Client) {
	h.mu.Lock()

	room, exists := h.rooms[c.roomCode]
	if !exists {
		room = make(map[int64]*Client)
		h.rooms[c.roomCode] = room
	}

	old, duplicated := room[c.accountID]
	room[c.accountID] = c

	h.mu.Unlock()

	if duplicated {
		h.logger.Info().
			Str("room_code", c.roomCode).
			Int64("account_id", c.accountID).
			Msg("同账号重复连接，旧连接被顶替")

		old.close()
	}
}

// unregister 注销连接。仅当当前登记的连接就是 c 时才移除 ——
// 避免「被顶掉的旧连接」在退出时误删新连接。
func (h *Hub) unregister(c *Client) {
	h.mu.Lock()

	room, exists := h.rooms[c.roomCode]
	if !exists {
		h.mu.Unlock()
		return
	}

	current, ok := room[c.accountID]
	if !ok || current != c {
		h.mu.Unlock()
		return
	}

	delete(room, c.accountID)
	if len(room) == 0 {
		delete(h.rooms, c.roomCode)
	}

	h.mu.Unlock()
}

// Broadcast 向房间内所有连接推送消息，可排除某个账号。
//
// 返回被断开（队列满）的连接数。
func (h *Hub) Broadcast(roomCode string, env Envelope, excludeAccountID int64) int {
	h.mu.RLock()
	room := h.rooms[roomCode]
	targets := make([]*Client, 0, len(room))
	for accountID, c := range room {
		if accountID == excludeAccountID {
			continue
		}

		targets = append(targets, c)
	}
	h.mu.RUnlock()

	dropped := 0
	for _, c := range targets {
		if !c.trySend(env) {
			dropped++
			h.logger.Warn().
				Str("room_code", roomCode).
				Int64("account_id", c.accountID).
				Msg("发送队列已满，断开该连接")

			c.close()
		}
	}

	return dropped
}

// SendTo 向房间内指定账号推送消息。目标不在线时返回 false。
func (h *Hub) SendTo(roomCode string, accountID int64, env Envelope) bool {
	h.mu.RLock()
	c, ok := h.rooms[roomCode][accountID]
	h.mu.RUnlock()

	if !ok {
		return false
	}

	if !c.trySend(env) {
		c.close()
		return false
	}

	return true
}

// ClientsIn 返回房间内全部连接（用于构造房间状态）。
func (h *Hub) ClientsIn(roomCode string) []*Client {
	h.mu.RLock()
	defer h.mu.RUnlock()

	room := h.rooms[roomCode]
	list := make([]*Client, 0, len(room))
	for _, c := range room {
		list = append(list, c)
	}

	return list
}

// CloseRoom 关闭房间内所有连接并广播房间关闭。
func (h *Hub) CloseRoom(roomCode, reason string) {
	env, err := newEnvelope(TypeRoomClosed, RoomClosedPayload{Reason: reason})
	if err != nil {
		h.logger.Error().Err(err).Msg("构造 room_closed 消息失败")
	}

	h.mu.Lock()
	room := h.rooms[roomCode]
	delete(h.rooms, roomCode)
	h.mu.Unlock()

	for _, c := range room {
		if err == nil {
			c.trySend(env)
		}

		c.close()
	}
}

// readPump 读循环：解析客户端消息并分发。
//
// 每个连接一个 goroutine。退出时负责注销连接并触发断线回调。
func (c *Client) readPump() {
	defer func() {
		c.hub.unregister(c)
		c.close()

		if c.hub.onDisconnect != nil {
			// 用独立 context：请求 context 在连接关闭时已取消，
			// 而房主迁移需要继续访问数据库。
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			c.hub.onDisconnect(ctx, c.roomCode, c.accountID)
		}
	}()

	c.conn.SetReadLimit(maxMessageSize)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	limiter := newMessageLimiter(messageRateLimit, time.Second)

	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				c.log.Debug().Err(err).Msg("信令连接异常关闭")
			}

			return
		}

		// 任何消息都算活动，刷新读超时。
		_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))

		if !limiter.allow() {
			c.sendError(ErrCodeRateLimited, "消息发送过快")
			continue
		}

		var env Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			c.sendError(ErrCodeInvalidMessage, "消息格式错误")
			continue
		}

		c.handle(env)
	}
}

// writePump 写循环：把发送队列里的消息写到连接上。
//
// 所有写操作集中在这一个 goroutine，避免并发写。
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.close()
	}()

	for {
		select {
		case env, ok := <-c.send:
			if !ok {
				return
			}

			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteJSON(env); err != nil {
				return
			}

		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}

		case <-c.closed:
			return
		}
	}
}

// sendError 向客户端推送错误消息。
func (c *Client) sendError(code, message string) {
	env, err := newEnvelope(TypeError, ErrorPayload{Code: code, Message: message})
	if err != nil {
		return
	}

	c.trySend(env)
}

// messageLimiter 固定窗口消息限流器（连接级）。
type messageLimiter struct {
	limit     int
	window    time.Duration
	count     int
	windowEnd time.Time
}

func newMessageLimiter(limit int, window time.Duration) *messageLimiter {
	return &messageLimiter{
		limit:     limit,
		window:    window,
		windowEnd: time.Now().Add(window),
	}
}

// allow 判断本条消息是否放行。
func (l *messageLimiter) allow() bool {
	now := time.Now()
	if now.After(l.windowEnd) {
		l.count = 0
		l.windowEnd = now.Add(l.window)
	}

	l.count++

	return l.count <= l.limit
}
