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

// contextAvatarURL gin.Context 中存放头像 URL 的键。
const contextAvatarURL = "avatar_url"

// DisplayNameResolver 按账号 ID 解析昵称。
//
// 昵称不放进 JWT：改名后旧令牌会一直带着旧昵称。
// 由调用方注入解析函数，middleware 不依赖 account 包（避免循环依赖）。
type DisplayNameResolver func(ctx context.Context, accountID int64) (string, error)

// Profile 账号展示信息（昵称 + 头像 URL）。
//
// 房间成员列表要展示头像与昵称，两者都从账号资料解析。
type Profile struct {
	DisplayName string
	AvatarURL   string
}

// ProfileResolver 按账号 ID 解析展示信息。
type ProfileResolver func(ctx context.Context, accountID int64) (Profile, error)

// RequireAuth 校验 Authorization: Bearer <access_token>。
//
// 校验通过后把账号 ID 写入上下文，后续 handler 用 AccountIDFrom 取。
// resolver 非空时顺带解析昵称（房间模块需要展示成员昵称）。
func RequireAuth(issuer *auth.TokenIssuer, resolver DisplayNameResolver) gin.HandlerFunc {
	return RequireAuthWithProfile(issuer, wrapDisplayNameResolver(resolver))
}

// RequireAuthWithProfile 与 RequireAuth 相同，但解析昵称 + 头像 URL。
//
// 房间模块需要头像，因此单独提供带 profile 的版本；
// 其余模块（如中继）不需要展示信息，传 nil 即可。
func RequireAuthWithProfile(issuer *auth.TokenIssuer, resolver ProfileResolver) gin.HandlerFunc {
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
			profile, err := resolver(c.Request.Context(), claims.AccountID)
			if err != nil {
				httpx.Unauthorized(c, httpx.CodeTokenInvalid, "账号不存在")
				return
			}

			c.Set(contextDisplayName, profile.DisplayName)
			c.Set(contextAvatarURL, profile.AvatarURL)
		}

		c.Next()
	}
}

// wrapDisplayNameResolver 把只解析昵称的 resolver 适配成 ProfileResolver。
func wrapDisplayNameResolver(resolver DisplayNameResolver) ProfileResolver {
	if resolver == nil {
		return nil
	}

	return func(ctx context.Context, accountID int64) (Profile, error) {
		name, err := resolver(ctx, accountID)
		if err != nil {
			return Profile{}, err
		}

		return Profile{DisplayName: name}, nil
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

// AvatarURLFrom 从上下文取当前账号头像 URL。未解析或未设置头像时返回空串。
func AvatarURLFrom(c *gin.Context) string {
	value, exists := c.Get(contextAvatarURL)
	if !exists {
		return ""
	}

	url, _ := value.(string)

	return url
}
