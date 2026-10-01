package middleware

import (
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tower/server/internal/httpx"
)

// RateLimit 按客户端 IP 限流。
//
// 内存计数器实现，够单机用。多实例部署时每个实例各算一份，
// 实际额度会翻倍 —— 上真实服务器前需换成 Redis 共享计数。
//
// 采用固定窗口而非令牌桶：实现简单、行为可预测，
// 对「防暴力破解」这个目标足够。
func RateLimit(limit int, window time.Duration) gin.HandlerFunc {
	type bucket struct {
		count     int
		expiresAt time.Time
	}

	var (
		mu      sync.Mutex
		buckets = make(map[string]*bucket)
	)

	// 惰性清理：每次请求顺手扫一遍过期桶。
	// 相比起后台 goroutine，省一个生命周期管理，代价是 O(n) 扫描 —— 单机量级可接受。
	cleanup := func(now time.Time) {
		for key, b := range buckets {
			if now.After(b.expiresAt) {
				delete(buckets, key)
			}
		}
	}

	return func(c *gin.Context) {
		key := c.ClientIP()
		now := time.Now()

		mu.Lock()
		cleanup(now)

		b, exists := buckets[key]
		if !exists || now.After(b.expiresAt) {
			buckets[key] = &bucket{count: 1, expiresAt: now.Add(window)}
			mu.Unlock()
			c.Next()
			return
		}

		b.count++
		exceeded := b.count > limit
		retryAfter := time.Until(b.expiresAt)
		mu.Unlock()

		if exceeded {
			c.Header("Retry-After", formatSeconds(retryAfter))
			httpx.TooManyRequests(c, "请求过于频繁，请稍后再试")
			return
		}

		c.Next()
	}
}

// formatSeconds 把剩余时间格式化为整秒字符串（向上取整，最小 1）。
func formatSeconds(d time.Duration) string {
	seconds := int(d.Seconds())
	if seconds < 1 {
		seconds = 1
	}

	return time.Duration(seconds * int(time.Second)).String()
}
