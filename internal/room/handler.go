package room

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"

	"github.com/tower/server/internal/httpx"
	"github.com/tower/server/internal/middleware"
)

// Handler 房间 HTTP 接口。
type Handler struct {
	service *Service
	logger  zerolog.Logger
}

// NewHandler 创建房间接口处理器。
func NewHandler(service *Service, logger zerolog.Logger) *Handler {
	return &Handler{service: service, logger: logger}
}

// Register 注册路由。全部需要认证。
//
// createLimit 只挂在创建接口上：创建房间是唯一会写库并占用房间码的操作，
// 也是最容易被刷的（同账号 5 次/分钟）。
func (h *Handler) Register(r gin.IRouter, auth gin.HandlerFunc, createLimit gin.HandlerFunc) {
	group := r.Group("/rooms", auth)

	group.POST("", createLimit, h.create)
	group.GET("", h.list)
	group.GET("/:code", h.get)
	group.POST("/:code/join", h.join)
	group.POST("/:code/leave", h.leave)
	group.DELETE("/:code", h.close)

	group.POST("/:code/session", h.createSession)
	group.GET("/:code/session", h.getSession)
	group.POST("/:code/session/ready", h.markSessionReady)
	group.DELETE("/:code/session", h.closeSession)
}

type createRequest struct {
	Name       string `json:"name" binding:"required"`
	MaxPlayers int    `json:"max_players"`
	IsPublic   *bool  `json:"is_public"`
}

// create 创建房间。
func (h *Handler) create(c *gin.Context) {
	accountID, ok := middleware.AccountIDFrom(c)
	if !ok {
		httpx.Unauthorized(c, httpx.CodeTokenInvalid, "未认证")
		return
	}

	var req createRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "请求体格式错误：name 为必填")
		return
	}

	// 默认值：4 人、公开。用指针区分「未传」与「显式传 false」。
	maxPlayers := req.MaxPlayers
	if maxPlayers == 0 {
		maxPlayers = maxPlayerLimit
	}

	isPublic := true
	if req.IsPublic != nil {
		isPublic = *req.IsPublic
	}

	displayName := middleware.DisplayNameFrom(c)
	avatarURL := middleware.AvatarURLFrom(c)

	snapshot, err := h.service.Create(c.Request.Context(), accountID, displayName, avatarURL, req.Name, maxPlayers, isPublic)
	if err != nil {
		h.writeError(c, err, "创建房间失败")
		return
	}

	c.JSON(http.StatusCreated, snapshot)
}

// list 房间列表。
func (h *Handler) list(c *gin.Context) {
	entries, err := h.service.List(c.Request.Context())
	if err != nil {
		h.writeError(c, err, "查询房间列表失败")
		return
	}

	c.JSON(http.StatusOK, gin.H{"rooms": entries})
}

// get 房间详情。
func (h *Handler) get(c *gin.Context) {
	snapshot, err := h.service.Get(c.Request.Context(), c.Param("code"))
	if err != nil {
		h.writeError(c, err, "查询房间失败")
		return
	}

	c.JSON(http.StatusOK, snapshot)
}

// join 加入房间。
func (h *Handler) join(c *gin.Context) {
	accountID, ok := middleware.AccountIDFrom(c)
	if !ok {
		httpx.Unauthorized(c, httpx.CodeTokenInvalid, "未认证")
		return
	}

	snapshot, err := h.service.Join(c.Request.Context(), c.Param("code"), accountID,
		middleware.DisplayNameFrom(c), middleware.AvatarURLFrom(c))
	if err != nil {
		h.writeError(c, err, "加入房间失败")
		return
	}

	c.JSON(http.StatusOK, snapshot)
}

// leave 离开房间。
func (h *Handler) leave(c *gin.Context) {
	accountID, ok := middleware.AccountIDFrom(c)
	if !ok {
		httpx.Unauthorized(c, httpx.CodeTokenInvalid, "未认证")
		return
	}

	snapshot, closed, err := h.service.Leave(c.Request.Context(), c.Param("code"), accountID)
	if err != nil {
		h.writeError(c, err, "离开房间失败")
		return
	}

	if closed {
		c.JSON(http.StatusOK, gin.H{"closed": true})
		return
	}

	c.JSON(http.StatusOK, gin.H{"closed": false, "room": snapshot})
}

// close 房主关闭房间。
func (h *Handler) close(c *gin.Context) {
	accountID, ok := middleware.AccountIDFrom(c)
	if !ok {
		httpx.Unauthorized(c, httpx.CodeTokenInvalid, "未认证")
		return
	}

	if err := h.service.Close(c.Request.Context(), c.Param("code"), accountID); err != nil {
		h.writeError(c, err, "关闭房间失败")
		return
	}

	c.Status(http.StatusNoContent)
}

// sessionReadyRequest 房主上报的监听地址。
type sessionReadyRequest struct {
	HostAddress string `json:"host_address" binding:"required"`
	HostPort    int    `json:"host_port" binding:"required"`
}

// createSession 房主创建本局游戏会话。
func (h *Handler) createSession(c *gin.Context) {
	accountID, ok := middleware.AccountIDFrom(c)
	if !ok {
		httpx.Unauthorized(c, httpx.CodeTokenInvalid, "未认证")
		return
	}

	session, err := h.service.CreateSession(c.Request.Context(), c.Param("code"), accountID)
	if err != nil {
		h.writeError(c, err, "创建游戏会话失败")
		return
	}

	c.JSON(http.StatusCreated, session)
}

// getSession 查询本局会话。
func (h *Handler) getSession(c *gin.Context) {
	session, ok := h.service.GetSession(c.Param("code"))
	if !ok {
		httpx.Fail(c, http.StatusNotFound, httpx.CodeRoomNotFound, "本局会话不存在")
		return
	}

	c.JSON(http.StatusOK, session)
}

// markSessionReady 房主上报监听地址，会话转为 ready。
func (h *Handler) markSessionReady(c *gin.Context) {
	accountID, ok := middleware.AccountIDFrom(c)
	if !ok {
		httpx.Unauthorized(c, httpx.CodeTokenInvalid, "未认证")
		return
	}

	var req sessionReadyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "请求体格式错误：host_address 与 host_port 为必填")
		return
	}

	session, err := h.service.MarkSessionReady(c.Param("code"), accountID, req.HostAddress, req.HostPort)
	if err != nil {
		h.writeError(c, err, "标记游戏会话就绪失败")
		return
	}

	c.JSON(http.StatusOK, session)
}

// closeSession 房主结束本局会话。
func (h *Handler) closeSession(c *gin.Context) {
	accountID, ok := middleware.AccountIDFrom(c)
	if !ok {
		httpx.Unauthorized(c, httpx.CodeTokenInvalid, "未认证")
		return
	}

	if err := h.service.CloseSession(c.Param("code"), accountID); err != nil {
		h.writeError(c, err, "关闭游戏会话失败")
		return
	}

	c.Status(http.StatusNoContent)
}

// writeError 把业务错误映射为 HTTP 响应。
func (h *Handler) writeError(c *gin.Context, err error, fallback string) {
	switch {
	case errors.Is(err, ErrInvalidName),
		errors.Is(err, ErrInvalidMaxPlayers),
		errors.Is(err, ErrInvalidCode):
		httpx.BadRequest(c, err.Error())

	case errors.Is(err, ErrRoomNotFound):
		httpx.Fail(c, http.StatusNotFound, httpx.CodeRoomNotFound, err.Error())

	case errors.Is(err, ErrSessionNotFound):
		httpx.Fail(c, http.StatusNotFound, httpx.CodeRoomNotFound, err.Error())

	case errors.Is(err, ErrAlreadyInRoom),
		errors.Is(err, ErrRoomFull),
		errors.Is(err, ErrRoomNotJoinable):
		httpx.Conflict(c, httpx.CodeRoomConflict, err.Error())

	case errors.Is(err, ErrNotHost),
		errors.Is(err, ErrNotMember):
		httpx.Fail(c, http.StatusForbidden, httpx.CodeForbidden, err.Error())

	default:
		h.logger.Error().Err(err).Str("path", c.Request.URL.Path).Msg(fallback)
		httpx.Internal(c, fallback)
	}
}
