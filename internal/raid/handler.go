package raid

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"

	"github.com/tower/server/internal/httpx"
	"github.com/tower/server/internal/middleware"
)

// maxFloor 撤离层数的合法上限。当前内容配置远小于此值，仅拦截明显异常。
const maxFloor = 10000

// Handler 撤离结算 HTTP 接口。
type Handler struct {
	repo   *Repository
	logger zerolog.Logger
}

// NewHandler 创建撤离结算处理器。
func NewHandler(repo *Repository, logger zerolog.Logger) *Handler {
	return &Handler{repo: repo, logger: logger}
}

// Register 注册路由。需要认证 —— 结算记录必须归属到具体账号。
//
// extractLimit 只挂在上报接口上：这是唯一写库操作，防止被刷。
func (h *Handler) Register(r gin.IRouter, auth gin.HandlerFunc, extractLimit gin.HandlerFunc) {
	group := r.Group("/raid", auth)
	group.POST("/extract", extractLimit, h.extract)
}

type extractRequest struct {
	Floor int           `json:"floor"`
	Items []ExtractItem `json:"items"`
}

// extract 上报撤离结算。响应为 204 无内容 —— 客户端 SendRaw 只关心 2xx。
func (h *Handler) extract(c *gin.Context) {
	accountID, ok := middleware.AccountIDFrom(c)
	if !ok {
		httpx.Unauthorized(c, httpx.CodeTokenInvalid, "未认证")
		return
	}

	var req extractRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "请求体格式错误：floor 与 items 为必填")
		return
	}

	if req.Floor < 1 || req.Floor > maxFloor {
		httpx.BadRequest(c, "floor 超出合法范围")
		return
	}

	if err := h.repo.Insert(c.Request.Context(), accountID, req.Floor, req.Items); err != nil {
		h.logger.Error().Err(err).Int64("account_id", accountID).Msg("保存撤离结算失败")
		httpx.Internal(c, "保存撤离结算失败")
		return
	}

	c.Status(http.StatusNoContent)
}
