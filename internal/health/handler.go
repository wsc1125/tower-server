// Package health 健康检查接口。
package health

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Handler 健康检查处理器。
type Handler struct {
	pool *pgxpool.Pool
}

// NewHandler 创建健康检查处理器。
func NewHandler(pool *pgxpool.Pool) *Handler {
	return &Handler{pool: pool}
}

// Register 注册健康检查路由。
//
// /healthz 存活检查：进程在跑就返回 200，不碰数据库。
// /readyz  就绪检查：数据库可连才返回 200，用于负载均衡摘除故障实例。
func (h *Handler) Register(r gin.IRouter) {
	r.GET("/healthz", h.Live)
	r.GET("/readyz", h.Ready)
}

// Live 存活检查。
func (h *Handler) Live(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// Ready 就绪检查：探测数据库连通性。
func (h *Handler) Ready(c *gin.Context) {
	if h.pool == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable", "reason": "数据库未初始化"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	if err := h.pool.Ping(ctx); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable", "reason": "数据库不可达"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ready"})
}
