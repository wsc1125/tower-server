package auth

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TokenType 令牌类型。access 用于访问接口，refresh 仅用于换取新令牌。
//
// 区分类型是必要的：否则 refresh token（有效期 30 天）可以直接当 access token 用，
// 等于把长期凭证暴露在每个请求里。
type TokenType string

const (
	// TokenTypeAccess 访问令牌。
	TokenTypeAccess TokenType = "access"

	// TokenTypeRefresh 刷新令牌。
	TokenTypeRefresh TokenType = "refresh"
)

// ErrTokenExpired 令牌已过期。
var ErrTokenExpired = errors.New("令牌已过期")

// ErrTokenInvalid 令牌无效（签名错误、格式错误、类型不符等）。
var ErrTokenInvalid = errors.New("令牌无效")

// Claims JWT 载荷。
//
// 只放 account_id 与类型 —— 用户名等信息随时可能变，放进令牌会导致改名后旧令牌仍带旧名。
type Claims struct {
	AccountID int64     `json:"-"`
	Type      TokenType `json:"type"`
	jwt.RegisteredClaims
}

// TokenIssuer 令牌签发与校验器。
type TokenIssuer struct {
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
}

// NewTokenIssuer 创建签发器。
func NewTokenIssuer(secret string, accessTTL, refreshTTL time.Duration) *TokenIssuer {
	return &TokenIssuer{
		secret:     []byte(secret),
		accessTTL:  accessTTL,
		refreshTTL: refreshTTL,
	}
}

// TokenPair 一次登录返回的双令牌。
type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
}

// Issue 为指定账号签发一对令牌。
func (i *TokenIssuer) Issue(accountID int64) (TokenPair, error) {
	access, err := i.sign(accountID, TokenTypeAccess, i.accessTTL)
	if err != nil {
		return TokenPair{}, err
	}

	refresh, err := i.sign(accountID, TokenTypeRefresh, i.refreshTTL)
	if err != nil {
		return TokenPair{}, err
	}

	return TokenPair{
		AccessToken:  access,
		RefreshToken: refresh,
		TokenType:    "Bearer",
		ExpiresIn:    int64(i.accessTTL.Seconds()),
	}, nil
}

// Parse 校验令牌并返回载荷。expected 指定期望的令牌类型，类型不符视为无效。
func (i *TokenIssuer) Parse(tokenString string, expected TokenType) (*Claims, error) {
	claims := &Claims{}

	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		// 只接受 HS256 —— 不校验算法会让攻击者用 "alg: none" 或 RS256 公钥伪造令牌。
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("非预期的签名算法: %v", t.Header["alg"])
		}

		return i.secret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrTokenExpired
		}

		return nil, ErrTokenInvalid
	}

	if !token.Valid {
		return nil, ErrTokenInvalid
	}

	if claims.Type != expected {
		return nil, ErrTokenInvalid
	}

	accountID, err := strconv.ParseInt(claims.Subject, 10, 64)
	if err != nil {
		return nil, ErrTokenInvalid
	}

	claims.AccountID = accountID

	return claims, nil
}

// sign 生成单个令牌。
func (i *TokenIssuer) sign(accountID int64, tokenType TokenType, ttl time.Duration) (string, error) {
	now := time.Now()

	claims := Claims{
		Type: tokenType,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatInt(accountID, 10),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}

	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(i.secret)
	if err != nil {
		return "", fmt.Errorf("签发令牌失败: %w", err)
	}

	return signed, nil
}
