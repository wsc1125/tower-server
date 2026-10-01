package signal

import (
	"encoding/json"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
)

// handle 分发客户端消息。
func (c *Client) handle(env Envelope) {
	switch env.Type {
	case TypePing:
		c.reply(TypePong, struct{}{}, env.Seq)

	case TypeJoinRoom:
		// 连接时已通过 query 参数加入房间，这里只回带当前状态。
		// 保留该消息类型是为了兼容客户端「连接后显式 join」的写法。
		c.sendRoomState(env.Seq)

	case TypeSignal:
		c.handleSignal(env)

	case TypeReady:
		c.handleReady(env)

	case TypeStartGame:
		c.handleStartGame(env)

	default:
		c.sendError(ErrCodeInvalidMessage, "未知的消息类型: "+string(env.Type))
	}
}

// handleSignal 转发信令数据给房间内指定玩家。
func (c *Client) handleSignal(env Envelope) {
	var payload SignalPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		c.sendError(ErrCodeInvalidMessage, "signal 载荷格式错误")
		return
	}

	if payload.TargetAccountID == 0 {
		c.sendError(ErrCodeInvalidMessage, "缺少 target_account_id")
		return
	}

	if len(payload.Data) > maxSignalDataSize {
		c.sendError(ErrCodePayloadTooLarge, "信令数据超过 64 KB 上限")
		return
	}

	forward, err := newEnvelope(TypeSignal, SignalForwardPayload{
		FromAccountID: c.accountID,
		Data:          payload.Data,
	})
	if err != nil {
		c.sendError(ErrCodeInternal, "构造转发消息失败")
		return
	}

	// 目标不在房间内时明确报错，而不是静默丢弃 ——
	// 客户端需要知道「对方已掉线」才能决定是否回退到中继。
	if !c.hub.SendTo(c.roomCode, payload.TargetAccountID, forward) {
		c.sendError(ErrCodeTargetNotFound, "目标玩家不在房间内或已掉线")
	}
}

// handleReady 更新准备状态并广播。
func (c *Client) handleReady(env Envelope) {
	var payload ReadyPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		c.sendError(ErrCodeInvalidMessage, "ready 载荷格式错误")
		return
	}

	c.setReady(payload.Ready)

	// 准备状态变化会影响所有人的界面（显示「3/4 已准备」），因此广播完整房间状态。
	c.hub.Broadcast(c.roomCode, c.mustEnvelope(TypeRoomState, c.roomStatePayload()), 0)
}

// handleStartGame 房主发起开始游戏。
func (c *Client) handleStartGame(env Envelope) {
	if !c.isHost() {
		c.sendError(ErrCodeNotHost, "只有房主可以开始游戏")
		return
	}

	payload := GameStartingPayload{HostAccountID: c.accountID}
	c.hub.Broadcast(c.roomCode, c.mustEnvelope(TypeGameStarting, payload), 0)

	c.log.Info().
		Str("room_code", c.roomCode).
		Int64("host_account", c.accountID).
		Msg("房主发起开始游戏")
}

// sendRoomState 向本连接下发完整房间状态。
func (c *Client) sendRoomState(seq int64) {
	env := c.mustEnvelope(TypeRoomState, c.roomStatePayload())
	env.Seq = seq

	c.trySend(env)
}

// roomStatePayload 构造房间状态载荷。
//
// 成员列表以 Hub 中的连接为准：只有真正连着信令的玩家才算「在房间里」。
// 这样客户端看到的成员列表与能收到消息的对象始终一致。
func (c *Client) roomStatePayload() RoomStatePayload {
	clients := c.hub.ClientsIn(c.roomCode)

	members := make([]MemberInfo, 0, len(clients))
	for _, other := range clients {
		members = append(members, MemberInfo{
			AccountID:   other.accountID,
			DisplayName: other.displayName,
			IsHost:      other.isHost(),
			Ready:       other.IsReady(),
		})
	}

	return RoomStatePayload{
		RoomCode:      c.roomCode,
		Members:       members,
		HostAccountID: c.hostAccountID(),
	}
}

// isHost 判断本连接是否属于房主。
func (c *Client) isHost() bool {
	return c.accountID == c.hostAccountID()
}

// hostAccountID 从 room 模块查询当前房主。
func (c *Client) hostAccountID() int64 {
	if c.hub.hostResolver == nil {
		return 0
	}

	return c.hub.hostResolver(c.roomCode)
}

// reply 回带一条消息，并原样返回请求的 seq。
func (c *Client) reply(msgType MessageType, payload any, seq int64) {
	env, err := newEnvelope(msgType, payload)
	if err != nil {
		return
	}

	env.Seq = seq
	c.trySend(env)
}

// mustEnvelope 构造消息，失败时返回错误信封（调用方无需处理错误）。
//
// 用于「载荷是内部结构体、序列化不可能失败」的场景。
func (c *Client) mustEnvelope(msgType MessageType, payload any) Envelope {
	env, err := newEnvelope(msgType, payload)
	if err != nil {
		c.log.Error().Err(err).Str("type", string(msgType)).Msg("构造消息失败")
		return Envelope{Type: TypeError}
	}

	return env
}

// newClient 创建连接对象。
func newClient(conn *websocket.Conn, hub *Hub, accountID int64, displayName, roomCode string, logger zerolog.Logger) *Client {
	return &Client{
		accountID:   accountID,
		displayName: displayName,
		roomCode:    roomCode,
		conn:        conn,
		hub:         hub,
		log:         logger,
		send:        make(chan Envelope, sendBuffer),
		closed:      make(chan struct{}),
	}
}
