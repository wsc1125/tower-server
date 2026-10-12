// Package signal 信令模块：WebSocket 连接管理 + 消息转发。
//
// 职责边界：只负责「交换地址」与「房间事件通知」，不转发游戏流量。
// 信令数据（payload.data）对服务端是不透明字符串 —— 内容由客户端定义
// （WebRTC SDP / ICE candidate 或 Unity Transport 自定义格式），
// 服务端换方案时零改动。
package signal

import "encoding/json"

// MessageType 消息类型。
type MessageType string

// 客户端 → 服务端。
const (
	// TypeJoinRoom 加入房间信令。WebSocket 连接后客户端发的首条消息。
	TypeJoinRoom MessageType = "join_room"

	// TypeSignal 转发信令数据给指定玩家。
	TypeSignal MessageType = "signal"

	// TypeReady 标记准备状态。
	TypeReady MessageType = "ready"

	// TypeStartGame 房主发起开始游戏。
	TypeStartGame MessageType = "start_game"

	// TypePing 保活。
	TypePing MessageType = "ping"
)

// 服务端 → 客户端。
const (
	// TypeRoomState 加入成功后下发完整房间状态。
	TypeRoomState MessageType = "room_state"

	// TypeMemberJoined 有玩家加入。
	TypeMemberJoined MessageType = "member_joined"

	// TypeMemberLeft 有玩家离开。
	TypeMemberLeft MessageType = "member_left"

	// TypeGameStarting 开始游戏，附带主机地址。
	TypeGameStarting MessageType = "game_starting"

	// TypeHostChanged 房主迁移，客户端据此执行权威接管。
	TypeHostChanged MessageType = "host_changed"

	// TypeRoomClosed 房间关闭。
	TypeRoomClosed MessageType = "room_closed"

	// TypeError 错误。
	TypeError MessageType = "error"

	// TypePong 保活响应。
	TypePong MessageType = "pong"
)

// Envelope 统一消息信封。
//
// Seq 由客户端自增，服务端原样回带 —— 用于请求-响应配对。
// 服务端主动推送的消息 Seq 为 0。
type Envelope struct {
	Type    MessageType     `json:"type"`
	Seq     int64           `json:"seq,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// 客户端 → 服务端 payload。

// JoinRoomPayload 加入房间。
type JoinRoomPayload struct {
	RoomCode string `json:"room_code"`
}

// SignalPayload 转发信令数据。
//
// Data 是不透明字符串：服务端不解析、不校验内容，只做转发。
type SignalPayload struct {
	TargetAccountID int64  `json:"target_account_id"`
	Data            string `json:"data"`
}

// ReadyPayload 准备状态。
type ReadyPayload struct {
	Ready bool `json:"ready"`
}

// 服务端 → 客户端 payload。

// MemberInfo 成员信息。
type MemberInfo struct {
	AccountID   int64  `json:"account_id"`
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url"`
	IsHost      bool   `json:"is_host"`
	Ready       bool   `json:"ready"`
}

// RoomStatePayload 加入成功后下发的完整房间状态。
type RoomStatePayload struct {
	RoomCode      string       `json:"room_code"`
	Members       []MemberInfo `json:"members"`
	HostAccountID int64        `json:"host_account_id"`
}

// MemberJoinedPayload 有玩家加入。
type MemberJoinedPayload struct {
	AccountID   int64  `json:"account_id"`
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url"`
}

// MemberLeftPayload 有玩家离开。
type MemberLeftPayload struct {
	AccountID int64 `json:"account_id"`
}

// SignalForwardPayload 转发来的信令数据。
type SignalForwardPayload struct {
	FromAccountID int64  `json:"from_account_id"`
	Data          string `json:"data"`
}

// GameStartingPayload 开始游戏。
type GameStartingPayload struct {
	HostAccountID int64 `json:"host_account_id"`
}

// HostChangedPayload 房主迁移。
type HostChangedPayload struct {
	NewHostAccountID     int64  `json:"new_host_account_id"`
	NewHostDisplayName   string `json:"new_host_display_name"`
}

// RoomClosedPayload 房间关闭。
type RoomClosedPayload struct {
	Reason string `json:"reason"`
}

// ErrorPayload 错误。
type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// 信令错误码。
const (
	// ErrCodeInvalidMessage 消息格式错误。
	ErrCodeInvalidMessage = "INVALID_MESSAGE"

	// ErrCodeNotInRoom 尚未加入房间。
	ErrCodeNotInRoom = "NOT_IN_ROOM"

	// ErrCodeTargetNotFound 目标玩家不在房间内。
	ErrCodeTargetNotFound = "TARGET_NOT_FOUND"

	// ErrCodeNotHost 只有房主可执行。
	ErrCodeNotHost = "NOT_HOST"

	// ErrCodeRateLimited 消息发送过快。
	ErrCodeRateLimited = "RATE_LIMITED"

	// ErrCodePayloadTooLarge 信令数据超过大小上限。
	ErrCodePayloadTooLarge = "PAYLOAD_TOO_LARGE"

	// ErrCodeInternal 服务端内部错误。
	ErrCodeInternal = "INTERNAL_ERROR"
)

// maxSignalDataSize 单条信令数据的大小上限（64 KB）。
//
// 防超大 payload 打爆内存：SDP 通常几 KB，64 KB 已远超正常需求。
const maxSignalDataSize = 64 * 1024

// newEnvelope 构造一条服务端消息。
func newEnvelope(msgType MessageType, payload any) (Envelope, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, err
	}

	return Envelope{Type: msgType, Payload: raw}, nil
}
