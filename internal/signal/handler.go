package signal

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"

	"github.com/tower/server/internal/auth"
	"github.com/tower/server/internal/httpx"
)

// upgrader WebSocket 升级器。
//
// CheckOrigin 恒返回 true：客户端是 Unity 游戏（非浏览器），
// 不存在 CSRF 场景；真正的鉴权靠 token 参数。
var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// RoomLookup 房间查询接口，由 room 模块实现。
//
// 定义在消费方（signal）而非实现方（room），
// 避免 signal 直接依赖 room 包。
type RoomLookup interface {
	// HostAccountID 返回房间当前房主；房间不存在时返回 0。
	HostAccountID(roomCode string) int64

	// IsMember 判断账号是否在房间内。
	IsMember(roomCode string, accountID int64) bool

	// OnDisconnect 处理连接断开，返回新房主信息供广播。
	//
	// 返回新房主信息而非让 signal 再查一次：房主迁移与断线处理是同一个原子操作，
	// 分开查会读到中间状态。
	OnDisconnect(roomCode string, accountID int64) DisconnectResult
}

// DisconnectResult 断线处理结果。
//
// 与 room.DisconnectResult 字段一致。Go 的接口要求方法签名完全匹配，
// 因此 room 包直接返回本类型（room 已 import signal 的语义方向相反，
// 实际由 main 包做适配）。
type DisconnectResult struct {
	// NewHostAccountID 迁移后的新房主；房主未变或房间关闭时为 0。
	NewHostAccountID int64

	// NewHostDisplayName 新房主昵称。
	NewHostDisplayName string

	// Closed 房间是否已关闭。
	Closed bool
}

// Handler 信令 WebSocket 接口。
type Handler struct {
	hub            *Hub
	issuer         *auth.TokenIssuer
	rooms          RoomLookup
	profileResolver func(accountID int64) Profile
	logger         zerolog.Logger
}

// Profile 账号展示信息（昵称 + 头像 URL）。
type Profile struct {
	DisplayName string
	AvatarURL   string
}

// NewHandler 创建信令接口处理器。
//
// profileResolver 用于在连接建立时解析昵称与头像（信令消息频率高，不能每条都查库）。
func NewHandler(hub *Hub, issuer *auth.TokenIssuer, rooms RoomLookup, profileResolver func(accountID int64) Profile, logger zerolog.Logger) *Handler {
	hub.SetHostResolver(rooms.HostAccountID)
	hub.SetDisconnectHandler(func(_ context.Context, roomCode string, accountID int64) {
		result := rooms.OnDisconnect(roomCode, accountID)

		// 房间已关闭：通知剩余连接（若有）并清理 Hub。
		if result.Closed {
			hub.CloseRoom(roomCode, "房间已关闭")
			return
		}

		// 广播「有人离开」。
		if env, err := newEnvelope(TypeMemberLeft, MemberLeftPayload{AccountID: accountID}); err == nil {
			hub.Broadcast(roomCode, env, 0)
		}

		// 房主迁移：通知剩余成员执行权威接管。
		if result.NewHostAccountID != 0 {
			payload := HostChangedPayload{
				NewHostAccountID:   result.NewHostAccountID,
				NewHostDisplayName: result.NewHostDisplayName,
			}

			if env, err := newEnvelope(TypeHostChanged, payload); err == nil {
				hub.Broadcast(roomCode, env, 0)
			}
		}
	})

	return &Handler{
		hub:             hub,
		issuer:          issuer,
		rooms:           rooms,
		profileResolver: profileResolver,
		logger:          logger,
	}
}

// Register 注册信令路由。
//
// 不走 RequireAuth 中间件：WebSocket 握手无法自定义 Authorization 头
// （Unity 的 ClientWebSocket 支持，但浏览器 WebSocket API 不支持），
// 因此 token 通过 query 参数传递，在 handler 内手动校验。
func (h *Handler) Register(r gin.IRouter) {
	r.GET("/signal", h.serve)
}

// serve 处理 WebSocket 升级。
//
// 路径：/api/v1/signal?room_code=XXX&token=YYY
func (h *Handler) serve(c *gin.Context) {
	roomCode := strings.ToUpper(strings.TrimSpace(c.Query("room_code")))
	token := strings.TrimSpace(c.Query("token"))

	if roomCode == "" {
		httpx.BadRequest(c, "缺少 room_code 参数")
		return
	}

	if token == "" {
		httpx.Unauthorized(c, httpx.CodeTokenInvalid, "缺少 token 参数")
		return
	}

	claims, err := h.issuer.Parse(token, auth.TokenTypeAccess)
	if err != nil {
		if err == auth.ErrTokenExpired {
			httpx.Unauthorized(c, httpx.CodeTokenExpired, "令牌已过期，请重新登录")
			return
		}

		httpx.Unauthorized(c, httpx.CodeTokenInvalid, "令牌无效")
		return
	}

	// 必须是房间成员才能连信令 —— 否则任何人都能凭房间码偷听信令。
	if !h.rooms.IsMember(roomCode, claims.AccountID) {
		httpx.Fail(c, http.StatusForbidden, httpx.CodeForbidden, "你不在该房间内")
		return
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		// Upgrade 失败时已自行写过响应，这里只记日志。
		h.logger.Warn().Err(err).Msg("WebSocket 升级失败")
		return
	}

	profile := h.profile(claims.AccountID)
	client := newClient(conn, h.hub, claims.AccountID, profile.DisplayName, profile.AvatarURL, roomCode, h.logger)

	h.hub.register(client)

	h.logger.Info().
		Str("room_code", roomCode).
		Int64("account_id", claims.AccountID).
		Msg("信令连接已建立")

	// 先启动写循环，再下发房间状态 —— 否则首条消息会因无人消费而卡在队列里。
	go client.writePump()

	client.sendRoomState(0)

	// 通知房间内其他人「有人进来了」。
	joined, err := newEnvelope(TypeMemberJoined, MemberJoinedPayload{
		AccountID:   claims.AccountID,
		DisplayName: client.displayName,
		AvatarURL:   client.avatarURL,
	})
	if err == nil {
		h.hub.Broadcast(roomCode, joined, claims.AccountID)
	}

	// readPump 阻塞直到连接关闭，其 defer 负责注销与断线回调。
	client.readPump()
}

// profile 查询账号展示信息（昵称 + 头像 URL）。
//
// 在连接建立时查一次并缓存到 Client 上：
// 信令消息频率高，每条都查库不可接受。
func (h *Handler) profile(accountID int64) Profile {
	if h.profileResolver == nil {
		return Profile{}
	}

	return h.profileResolver(accountID)
}
