package middleware

import (
	"context"
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/tower/server/internal/auth"
	"github.com/tower/server/internal/httpx"
)

// contextAccountID gin.Context 中存放账号 ID 的键。
const contextAccountID = "account_id"

// contextDisplayName gin.Context 中存放昵称的键。
const contextDisplayName = "display_name"

// DisplayNameResolver 按账号 ID 解析昵称。
//
// 昵称不放进 JWT：改名后旧令牌会一直带着旧昵称。
// 由调用方注入解析函数，middleware 不依赖 account 包（避免循环依赖）。
type DisplayNameResolver func(ctx context.Context, accountID int64) (string, error)

// RequireAuth 校验 Authorization: Bearer <access_token>。
//
// 校验通过后把账号 ID 写入上下文，后续 handler 用 AccountIDFrom 取。
// resolver 非空时顺带解析昵称（房间模块需要展示成员昵称）。
func RequireAuth(issuer *auth.TokenIssuer, resolver DisplayNameResolver) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if header == "" {
			httpx.Unauthorized(c, httpx.CodeTokenInvalid, "缺少 Authorization 头")
			return
		}

		// 允许 "Bearer xxx" 与裸 token 两种写法，大小写不敏感。
		token := header
		if len(header) > 7 && strings.EqualFold(header[:7], "bearer ") {
			token = strings.TrimSpace(header[7:])
		}

		claims, err := issuer.Parse(token, auth.TokenTypeAccess)
		if err != nil {
			if errors.Is(err, auth.ErrTokenExpired) {
				httpx.Unauthorized(c, httpx.CodeTokenExpired, "令牌已过期，请重新登录")
				return
			}

			httpx.Unauthorized(c, httpx.CodeTokenInvalid, "令牌无效")
			return
		}

		c.Set(contextAccountID, claims.AccountID)

		if resolver != nil {
			displayName, err := resolver(c.Request.Context(), claims.AccountID)
			if err != nil {
				httpx.Unauthorized(c, httpx.CodeTokenInvalid, "账号不存在")
				return
			}

			c.Set(contextDisplayName, displayName)
		}

		c.Next()
	}
}

// AccountIDFrom 从上下文取当前账号 ID。
func AccountIDFrom(c *gin.Context) (int64, bool) {
	value, exists := c.Get(contextAccountID)
	if !exists {
		return 0, false
	}

	accountID, ok := value.(int64)

	return accountID, ok
}

// DisplayNameFrom 从上下文取当前账号昵称。未解析时返回空串。
func DisplayNameFrom(c *gin.Context) string {
	value, exists := c.Get(contextDisplayName)
	if !exists {
		return ""
	}

	name, _ := value.(string)

	return name
}
