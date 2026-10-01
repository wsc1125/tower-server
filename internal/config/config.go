                                         package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config 服务端运行配置。全部来自环境变量（12-Factor），
// 本地开发用 .env 文件补齐，生产环境由容器编排注入。
type Config struct {
	// HTTPAddr HTTP 监听地址，如 ":8080"。
	HTTPAddr string

	// DatabaseURL PostgreSQL 连接串。
	DatabaseURL string

	// JWTSecret JWT 签名密钥（HS256）。生产环境必须是 32 字节以上随机值。
	JWTSecret string

	// AccessTokenTTL Access Token 有效期。
	AccessTokenTTL time.Duration

	// RefreshTokenTTL Refresh Token 有效期。
	RefreshTokenTTL time.Duration

	// BcryptCost bcrypt 哈希成本。12 约 250ms/次，平衡安全与体验。
	BcryptCost int

	// TURNSharedSecret 与 coturn 共享的密钥，用于生成临时凭证（RFC 5766）。
	//
	// 不把长期凭证下发给客户端：长期凭证一旦泄露，任何人都能白嫖中继带宽。
	TURNSharedSecret string

	// TURNRealm coturn 的 realm。
	TURNRealm string

	// TURNHost coturn 对外可达的主机名或 IP。
	//
	// 本地开发用 127.0.0.1（只能本机测试）；
	// 上云后填公网域名，客户端才能连上。
	TURNHost string

	// TURNPort coturn 的 STUN/TURN 监听端口。
	TURNPort int

	// TURNTTL 临时凭证有效期。
	TURNTTL time.Duration

	// AvatarDir 头像文件存储目录。
	AvatarDir string

	// PublicBaseURL 服务端对外可达的基地址，用于拼接头像 URL。
	//
	// 本地开发用 http://localhost:8080；上云后填公网域名，
	// 否则客户端拿到的头像地址指向 localhost，图片加载不出来。
	PublicBaseURL string

	// Env 运行环境：development / production。
	Env string
}

// IsProduction 是否生产环境。生产环境会收紧日志与错误输出。
func (c Config) IsProduction() bool {
	return c.Env == "production"
}

// Load 从环境变量读取配置并校验。缺失必填项时返回错误，避免服务带着半截配置启动。
func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:         getEnv("HTTP_ADDR", ":8080"),
		DatabaseURL:      os.Getenv("DATABASE_URL"),
		JWTSecret:        os.Getenv("JWT_SECRET"),
		AccessTokenTTL:   getDuration("ACCESS_TOKEN_TTL", time.Hour),
		RefreshTokenTTL:  getDuration("REFRESH_TOKEN_TTL", 30*24*time.Hour),
		BcryptCost:       getInt("BCRYPT_COST", 12),
		TURNSharedSecret: os.Getenv("TURN_SHARED_SECRET"),
		TURNRealm:        getEnv("TURN_REALM", "tower.local"),
		TURNHost:         getEnv("TURN_HOST", "127.0.0.1"),
		TURNPort:         getInt("TURN_PORT", 3478),
		TURNTTL:          getDuration("TURN_TTL", 24*time.Hour),
		AvatarDir:        getEnv("AVATAR_DIR", "data/avatars"),
		PublicBaseURL:    getEnv("PUBLIC_BASE_URL", "http://localhost:8080"),
		Env:              getEnv("APP_ENV", "development"),
	}

	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("环境变量 DATABASE_URL 未设置")
	}

	if cfg.JWTSecret == "" {
		return Config{}, fmt.Errorf("环境变量 JWT_SECRET 未设置")
	}

	if cfg.TURNSharedSecret == "" {
		return Config{}, fmt.Errorf("环境变量 TURN_SHARED_SECRET 未设置")
	}

	// 生产环境拒绝弱密钥：短密钥可被暴力破解，等于没有签名。
	if cfg.IsProduction() && len(cfg.JWTSecret) < 32 {
		return Config{}, fmt.Errorf("生产环境的 JWT_SECRET 必须至少 32 字节，当前 %d 字节", len(cfg.JWTSecret))
	}

	if cfg.IsProduction() && len(cfg.TURNSharedSecret) < 32 {
		return Config{}, fmt.Errorf("生产环境的 TURN_SHARED_SECRET 必须至少 32 字节，当前 %d 字节", len(cfg.TURNSharedSecret))
	}

	if cfg.BcryptCost < 4 || cfg.BcryptCost > 31 {
		return Config{}, fmt.Errorf("BCRYPT_COST 必须在 4~31 之间，当前 %d", cfg.BcryptCost)
	}

	if cfg.TURNPort < 1 || cfg.TURNPort > 65535 {
		return Config{}, fmt.Errorf("TURN_PORT 必须在 1~65535 之间，当前 %d", cfg.TURNPort)
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}

	return fallback
}

func getInt(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}

	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}

	return value
}

func getDuration(key string, fallback time.Duration) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}

	value, err := time.ParseDuration(raw)
	if err != nil {
		return fallback
	}

	return value
}
