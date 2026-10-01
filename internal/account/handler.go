package account

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"

	"github.com/tower/server/internal/auth"
	"github.com/tower/server/internal/avatar"
	"github.com/tower/server/internal/httpx"
	"github.com/tower/server/internal/middleware"
)

// registerRequest JSON 形式的注册请求体。multipart 形式见 parseMultipartRegister。
type registerRequest struct {
	Username    string `json:"username" binding:"required"`
	Password    string `json:"password" binding:"required"`
	DisplayName string `json:"display_name"`
}

// Handler 账号 HTTP 接口。
type Handler struct {
	service *Service
	issuer  *auth.TokenIssuer
	logger  zerolog.Logger
}

// NewHandler 创建账号接口处理器。
func NewHandler(service *Service, issuer *auth.TokenIssuer, logger zerolog.Logger) *Handler {
	return &Handler{service: service, issuer: issuer, logger: logger}
}

// Register 注册路由。
//
// 注册与登录挂限流：这两个接口是暴力破解与批量注册的主要目标。
func (h *Handler) Register(r gin.IRouter, loginLimit, registerLimit gin.HandlerFunc) {
	group := r.Group("/auth")

	group.POST("/register", registerLimit, h.register)
	group.POST("/login", loginLimit, h.login)
	group.POST("/refresh", h.refresh)

	// /me 需要携带 access token。这里不需要昵称，传 nil 跳过解析。
	group.GET("/me", middleware.RequireAuth(h.issuer, nil), h.me)
}

type loginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

// register 注册账号。
//
// 同时接受两种请求体：
//   - application/json：不带头像，字段为 username / password / display_name；
//   - multipart/form-data：带头像，同名字段 + avatar 文件。
//
// 之所以不拆成「先传头像拿 URL，再注册」两步：那会留下无人认领的孤儿文件，
// 还得额外做清理任务。一次请求里传完，失败就整体失败。
func (h *Handler) register(c *gin.Context) {
	username, password, displayName, avatarData, err := parseRegisterRequest(c)
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}

	acc, tokens, err := h.service.Register(c.Request.Context(), username, password, displayName, avatarData)
	if err != nil {
		h.writeServiceError(c, err, "注册失败")
		return
	}

	h.logger.Info().Int64("account_id", acc.ID).Str("username", acc.Username).
		Bool("has_avatar", acc.AvatarPath != "").Msg("账号注册成功")

	c.JSON(http.StatusCreated, gin.H{
		"account": acc,
		"tokens":  tokens,
	})
}

// parseRegisterRequest 从 JSON 或 multipart 请求里取出注册字段。
func parseRegisterRequest(c *gin.Context) (username, password, displayName string, avatarData []byte, err error) {
	if strings.HasPrefix(c.ContentType(), "multipart/form-data") {
		return parseMultipartRegister(c)
	}

	var req registerRequest
	if bindErr := c.ShouldBindJSON(&req); bindErr != nil {
		return "", "", "", nil, errors.New("请求体格式错误：username 与 password 为必填")
	}

	return req.Username, req.Password, req.DisplayName, nil, nil
}

// parseMultipartRegister 解析 multipart 注册请求。avatar 字段可缺省。
func parseMultipartRegister(c *gin.Context) (username, password, displayName string, avatarData []byte, err error) {
	username = c.PostForm("username")
	password = c.PostForm("password")
	displayName = c.PostForm("display_name")

	if username == "" || password == "" {
		return "", "", "", nil, errors.New("请求体格式错误：username 与 password 为必填")
	}

	file, header, fileErr := c.Request.FormFile("avatar")
	if fileErr != nil {
		// 没传头像不算错误 —— 头像是可选项。
		return username, password, displayName, nil, nil
	}
	defer file.Close()

	// 先按声明大小挡一道，避免把超大文件整个读进内存。
	if header.Size > avatar.MaxBytes {
		return "", "", "", nil, ErrInvalidAvatar
	}

	data, readErr := avatar.ReadAll(file)
	if readErr != nil {
		if errors.Is(readErr, avatar.ErrTooLarge) {
			return "", "", "", nil, ErrInvalidAvatar
		}

		return "", "", "", nil, errors.New("读取头像文件失败")
	}

	return username, password, displayName, data, nil
}

// login 登录。
func (h *Handler) login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "请求体格式错误：username 与 password 为必填")
		return
	}

	acc, tokens, err := h.service.Login(c.Request.Context(), req.Username, req.Password)
	if err != nil {
		h.writeServiceError(c, err, "登录失败")
		return
	}

	h.logger.Info().Int64("account_id", acc.ID).Msg("账号登录成功")

	h.service.Decorate(acc)

	c.JSON(http.StatusOK, gin.H{
		"account": acc,
		"tokens":  tokens,
	})
}

// refresh 用 refresh token 换新令牌。
func (h *Handler) refresh(c *gin.Context) {
	var req refreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "请求体格式错误：refresh_token 为必填")
		return
	}

	acc, tokens, err := h.service.Refresh(c.Request.Context(), req.RefreshToken)
	if err != nil {
		h.writeServiceError(c, err, "刷新令牌失败")
		return
	}

	h.service.Decorate(acc)

	c.JSON(http.StatusOK, gin.H{
		"account": acc,
		"tokens":  tokens,
	})
}

// me 返回当前登录账号的资料。
func (h *Handler) me(c *gin.Context) {
	accountID, ok := middleware.AccountIDFrom(c)
	if !ok {
		httpx.Unauthorized(c, httpx.CodeTokenInvalid, "未认证")
		return
	}

	acc, err := h.service.Profile(c.Request.Context(), accountID)
	if err != nil {
		h.writeServiceError(c, err, "查询账号失败")
		return
	}

	h.service.Decorate(acc)

	c.JSON(http.StatusOK, gin.H{"account": acc})
}

// writeServiceError 把业务错误映射为 HTTP 响应。
//
// 映射集中在一处，避免每个 handler 各写一套 if-else 导致状态码不一致。
func (h *Handler) writeServiceError(c *gin.Context, err error, fallback string) {
	switch {
	case errors.Is(err, ErrInvalidUsername),
		errors.Is(err, ErrInvalidPassword),
		errors.Is(err, ErrInvalidDisplayName),
		errors.Is(err, ErrInvalidAvatar):
		httpx.BadRequest(c, err.Error())

	case errors.Is(err, ErrUsernameTaken):
		httpx.Conflict(c, httpx.CodeUsernameTaken, err.Error())

	case errors.Is(err, ErrInvalidCredentials):
		httpx.Unauthorized(c, httpx.CodeInvalidCredentials, err.Error())

	case errors.Is(err, auth.ErrTokenExpired):
		httpx.Unauthorized(c, httpx.CodeTokenExpired, "令牌已过期，请重新登录")

	case errors.Is(err, auth.ErrTokenInvalid):
		httpx.Unauthorized(c, httpx.CodeTokenInvalid, "令牌无效")

	case errors.Is(err, ErrNotFound):
		httpx.Unauthorized(c, httpx.CodeTokenInvalid, "账号不存在")

	default:
		// 内部错误细节只进日志，不回给客户端 —— 数据库错误信息可能泄露表结构。
		h.logger.Error().Err(err).Str("path", c.Request.URL.Path).Msg(fallback)
		httpx.Internal(c, fallback)
	}
}
