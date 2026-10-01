package relay

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"

	"github.com/tower/server/internal/httpx"
	"github.com/tower/server/internal/middleware"
)

// Handler 中继凭证 HTTP 接口。
type Handler struct {
	issuer *Issuer
	logger zerolog.Logger
}

// NewHandler 创建中继凭证处理器。
func NewHandler(issuer *Issuer, logger zerolog.Logger) *Handler {
	return &Handler{issuer: issuer, logger: logger}
}

// Register 注册路由。需要认证 —— 凭证绑定账号，匿名签发等于开放中继。
func (h *Handler) Register(r gin.IRouter, auth gin.HandlerFunc) {
	r.GET("/relay/credentials", auth, h.credentials)
}

// credentials 下发 STUN/TURN 地址与临时凭证。
func (h *Handler) credentials(c *gin.Context) {
	accountID, ok := middleware.AccountIDFrom(c)
	if !ok {
		httpx.Unauthorized(c, httpx.CodeTokenInvalid, "未认证")
		return
	}

	c.JSON(http.StatusOK, h.issuer.BuildResponse(accountID))
}
