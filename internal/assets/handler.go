package assets

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"

	"github.com/tower/server/internal/httpx"
	"github.com/tower/server/internal/middleware"
)

// maxPayloadBytes 快照请求体大小上限。当前快照约几 KB，仅防滥用。
const maxPayloadBytes = 256 * 1024

// maxSchemaVersion 服务端已知的最高快照信封版本。
// 客户端上报更高版本时拒绝，避免旧服务端静默吞掉新字段。
const maxSchemaVersion = 1

// Handler 资产快照 HTTP 接口。
type Handler struct {
	repo   *Repository
	logger zerolog.Logger
}

// NewHandler 创建资产快照处理器。
func NewHandler(repo *Repository, logger zerolog.Logger) *Handler {
	return &Handler{repo: repo, logger: logger}
}

// Register 注册路由。全部需要认证 —— 快照必须归属到具体账号。
//
// putLimit 只挂在上传接口上：这是唯一写库操作，防止被刷。
func (h *Handler) Register(r gin.IRouter, auth gin.HandlerFunc, putLimit gin.HandlerFunc) {
	group := r.Group("/assets", auth)

	group.GET("/snapshot", h.get)
	group.PUT("/snapshot", putLimit, h.put)
}

// getResponse GET /assets/snapshot 响应。
//
// 服务端尚无快照时 revision = 0 且省略 data —— 客户端据此走初始装备流程。
// 注意：客户端用 JsonUtility 解析，它会把 "data":null 物化成完整空对象图，
// 因此「尚无快照」的判定信号是 revision == 0，而不是 JSON 的 null。
type getResponse struct {
	SchemaVersion int             `json:"schema_version"`
	Revision      int64           `json:"revision"`
	UpdatedAt     string          `json:"updated_at,omitempty"`
	Data          json.RawMessage `json:"data,omitempty"`
}

// get 下发当前账号的资产快照。
func (h *Handler) get(c *gin.Context) {
	accountID, ok := middleware.AccountIDFrom(c)
	if !ok {
		httpx.Unauthorized(c, httpx.CodeTokenInvalid, "未认证")
		return
	}

	snapshot, err := h.repo.Get(c.Request.Context(), accountID)
	if err != nil {
		h.logger.Error().Err(err).Int64("account_id", accountID).Msg("读取资产快照失败")
		httpx.Internal(c, "读取资产快照失败")
		return
	}

	if snapshot == nil {
		// 首次登录：无快照。revision=0 是客户端识别「新账号」的信号。
		c.JSON(http.StatusOK, getResponse{
			SchemaVersion: maxSchemaVersion,
			Revision:      0,
		})
		return
	}

	c.JSON(http.StatusOK, getResponse{
		SchemaVersion: snapshot.SchemaVersion,
		Revision:      snapshot.Revision,
		UpdatedAt:     snapshot.UpdatedAt,
		Data:          snapshot.Data,
	})
}

// putRequest PUT /assets/snapshot 请求。
type putRequest struct {
	SchemaVersion int             `json:"schema_version"`
	Data          json.RawMessage `json:"data"`
}

// putResponse PUT /assets/snapshot 响应。
type putResponse struct {
	Revision  int64  `json:"revision"`
	UpdatedAt string `json:"updated_at"`
}

// put 整包上传资产快照（幂等，可安全重试）。
func (h *Handler) put(c *gin.Context) {
	accountID, ok := middleware.AccountIDFrom(c)
	if !ok {
		httpx.Unauthorized(c, httpx.CodeTokenInvalid, "未认证")
		return
	}

	// 先按大小上限截断读取，避免超大请求体打满内存。
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxPayloadBytes)

	var req putRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// MaxBytesReader 超限时返回的错误也走这里，统一提示。
		httpx.BadRequest(c, "请求体格式错误或超过大小上限")
		return
	}

	if len(req.Data) == 0 {
		httpx.BadRequest(c, "请求体格式错误：data 为必填")
		return
	}

	if req.SchemaVersion < 1 || req.SchemaVersion > maxSchemaVersion {
		httpx.Fail(c, http.StatusBadRequest, httpx.CodeSchemaVersionUnsupported,
			"快照版本不受支持，请升级客户端")
		return
	}

	revision, err := h.repo.Upsert(c.Request.Context(), accountID, req.SchemaVersion, req.Data)
	if err != nil {
		h.logger.Error().Err(err).Int64("account_id", accountID).Msg("保存资产快照失败")
		httpx.Internal(c, "保存资产快照失败")
		return
	}

	c.JSON(http.StatusOK, putResponse{Revision: revision})
}
