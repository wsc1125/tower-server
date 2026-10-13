package relayws

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"

	"github.com/tower/server/internal/auth"
	"github.com/tower/server/internal/httpx"
)

// upgrader WebSocket 升级器。客户端是 Unity 游戏，不存在 CSRF 场景，鉴权靠 token 参数。
var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// RoomLookup 房间查询接口，由 room 模块实现。
type RoomLookup interface {
	// HostAccountID 返回房间当前房主；房间不存在时返回 0。
	HostAccountID(roomCode string) int64

	// IsMember 判断账号是否在房间内。
	IsMember(roomCode string, accountID int64) bool
}

// Handler 中继 WebSocket 接口。
type Handler struct {
	hub    *Hub
	issuer *auth.TokenIssuer
	rooms  RoomLookup
	logger zerolog.Logger
}

// NewHandler 创建中继接口处理器。
func NewHandler(hub *Hub, issuer *auth.TokenIssuer, rooms RoomLookup, logger zerolog.Logger) *Handler {
	return &Handler{hub: hub, issuer: issuer, rooms: rooms, logger: logger}
}

// Register 注册中继路由。
//
// 与信令相同不走 RequireAuth 中间件：token 通过 query 参数传递。
func (h *Handler) Register(r gin.IRouter) {
	r.GET("/relay-ws", h.serve)
}

// serve 处理 WebSocket 升级。
//
// 路径：/api/v1/relay-ws?room_code=XXX&token=YYY
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

	// 必须是房间成员才能连中继 —— 否则任何人都能凭房间码蹭流量。
	if !h.rooms.IsMember(roomCode, claims.AccountID) {
		httpx.Fail(c, http.StatusForbidden, httpx.CodeForbidden, "你不在该房间内")
		return
	}

	// 房间必须存在：中继拓扑按房主组织，没有房主就没有转发的目的地。
	hostAccountID := h.rooms.HostAccountID(roomCode)
	if hostAccountID == 0 {
		httpx.Fail(c, http.StatusNotFound, httpx.CodeRoomNotFound, "房间不存在")
		return
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		h.logger.Warn().Err(err).Msg("中继 WebSocket 升级失败")
		return
	}

	relayConn := &Conn{
		ws:        conn,
		send:      make(chan outbound, sendBuffer),
		hub:       h.hub,
		accountID: claims.AccountID,
		closed:    make(chan struct{}),
	}

	// NGO 侧的传输层 clientId：房主恒为 0（NetworkManager.ServerClientId），
	// 成员用账号 ID（正值，不会与 0 冲突，且同账号重连时 ID 稳定）。
	isHost := claims.AccountID == hostAccountID
	clientID := claims.AccountID
	if isHost {
		clientID = 0
	}

	h.hub.Join(roomCode, relayConn, isHost)

	// 先启动写循环，再下发 welcome —— 否则首条消息会卡在队列里。
	go relayConn.writePump()

	relayConn.enqueueWelcome(clientID)

	h.logger.Info().
		Str("room_code", roomCode).
		Int64("account_id", claims.AccountID).
		Int64("client_id", clientID).
		Msg("中继连接已建立")

	// readPump 阻塞直到连接关闭，其 defer 负责注销与房间清理。
	relayConn.readPump()
}
