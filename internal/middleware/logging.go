// Package middleware 提供 HTTP 中间件：请求日志、JWT 校验、限流。
package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
)

// Logging 结构化请求日志。
//
// 记录 method / path / status / latency / client_ip，
// 并透传 request_id 便于跨日志追踪。
// 禁止记录请求体 —— 里面可能有密码。
func Logging(logger zerolog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		query := c.Request.URL.RawQuery

		c.Next()

		event := logger.Info()
		if c.Writer.Status() >= 500 {
			event = logger.Error()
		} else if c.Writer.Status() >= 400 {
			event = logger.Warn()
		}

		event.
			Str("method", c.Request.Method).
			Str("path", path).
			Str("query", query).
			Int("status", c.Writer.Status()).
			Str("client_ip", c.ClientIP()).
			Float64("latency_ms", float64(time.Since(start).Microseconds())/1000.0).
			Msg("http_request")
	}
}

// Recovery 捕获 panic，返回 500 而不是让连接挂掉。
func Recovery(logger zerolog.Logger) gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, recovered any) {
		logger.Error().
			Interface("panic", recovered).
			Str("path", c.Request.URL.Path).
			Msg("panic_recovered")

		c.AbortWithStatus(500)
	})
}
