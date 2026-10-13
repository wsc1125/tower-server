// Command api 是爬塔联机服务端的入口。
//
// 职责边界（见《Go后端设计.md》第一章）：
// 账号 + 房间 + 信令 + 中继。不碰任何游戏逻辑 —— 战斗由房主客户端裁决。
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/rs/zerolog"

	"github.com/tower/server/internal/account"
	"github.com/tower/server/internal/auth"
	"github.com/tower/server/internal/avatar"
	"github.com/tower/server/internal/config"
	"github.com/tower/server/internal/db"
	"github.com/tower/server/internal/health"
	"github.com/tower/server/internal/middleware"
	"github.com/tower/server/internal/raid"
	"github.com/tower/server/internal/relay"
	"github.com/tower/server/internal/relayws"
	"github.com/tower/server/internal/room"
	signalpkg "github.com/tower/server/internal/signal"
)

func main() {
	// 本地开发用 .env 补齐环境变量；文件不存在不是错误（生产环境靠容器注入）。
	_ = godotenv.Load()

	logger := newLogger()

	cfg, err := config.Load()
	if err != nil {
		logger.Fatal().Err(err).Msg("配置加载失败")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Fatal().Err(err).Msg("数据库连接失败")
	}
	defer pool.Close()

	logger.Info().Str("env", cfg.Env).Msg("数据库已连接")

	// 启动即迁移：避免「忘了跑迁移」导致表不存在。
	if err := db.Migrate(ctx, pool); err != nil {
		logger.Fatal().Err(err).Msg("数据库迁移失败")
	}

	logger.Info().Msg("数据库迁移完成")

	// 房间注册表是进程内单例：HTTP 路由与后台清理任务共用同一份。
	roomRegistry := room.NewRegistry()

	router := newRouter(cfg, logger, pool, roomRegistry)

	// 房间超时清理：waiting 且 2 小时无活动的房间自动关闭。
	// 用独立 context 而非请求 context —— 它要活到进程退出。
	cleanupCtx, stopCleanup := context.WithCancel(context.Background())
	defer stopCleanup()

	go runRoomCleanup(cleanupCtx, room.NewService(room.NewRepository(pool), roomRegistry, logger), logger)

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info().Str("addr", cfg.HTTPAddr).Msg("HTTP 服务已启动")
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatal().Err(err).Msg("HTTP 服务异常退出")
		}
	}()

	<-ctx.Done()
	logger.Info().Msg("收到退出信号，开始优雅关闭")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error().Err(err).Msg("优雅关闭超时")
	}

	logger.Info().Msg("服务已停止")
}

// newLogger 创建结构化日志器。生产环境输出 JSON 供日志系统采集，开发环境输出可读文本。
func newLogger() zerolog.Logger {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnixMs

	if os.Getenv("APP_ENV") == "production" {
		return zerolog.New(os.Stdout).With().Timestamp().Logger()
	}

	return zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}).
		With().Timestamp().Logger()
}

// newRouter 装配路由与中间件。
func newRouter(cfg config.Config, logger zerolog.Logger, pool *pgxpool.Pool, registry *room.Registry) *gin.Engine {
	if cfg.IsProduction() {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.New()
	router.Use(middleware.Recovery(logger))
	router.Use(middleware.Logging(logger))

	// 健康检查挂在根路径，不带 /api/v1 前缀 —— 负载均衡与容器探针按惯例访问根路径。
	health.NewHandler(pool).Register(router)

	api := router.Group("/api/v1")

	// 账号模块：仓储 → 服务 → 接口。
	hasher := auth.NewPasswordHasher(cfg.BcryptCost)
	issuer := auth.NewTokenIssuer(cfg.JWTSecret, cfg.AccessTokenTTL, cfg.RefreshTokenTTL)
	accountRepo := account.NewRepository(pool)

	avatarStore, err := avatar.NewStore(cfg.AvatarDir)
	if err != nil {
		logger.Fatal().Err(err).Msg("初始化头像存储失败")
	}

	accountService := account.NewService(accountRepo, hasher, issuer, avatarStore, cfg.PublicBaseURL)

	// 头像静态文件：路径由服务端生成（<account_id>/<时间戳>.jpg），
	// 且 avatar.Store 会拒绝含 ".." 的路径，因此这里直接挂目录即可。
	router.Static("/avatars", avatarStore.Root())

	// 限流额度：登录 5 次/分（防暴力破解），注册 3 次/时（防批量注册）。
	loginLimit := middleware.RateLimit(5, time.Minute)
	registerLimit := middleware.RateLimit(3, time.Hour)

	account.NewHandler(accountService, issuer, logger).Register(api, loginLimit, registerLimit)

	// 房间模块。认证中间件带资料解析 —— 房间成员列表要展示昵称与头像。
	roomService := room.NewService(room.NewRepository(pool), registry, logger)
	roomAuth := middleware.RequireAuthWithProfile(issuer, func(ctx context.Context, accountID int64) (middleware.Profile, error) {
		acc, err := accountService.Profile(ctx, accountID)
		if err != nil {
			return middleware.Profile{}, err
		}

		accountService.Decorate(acc)

		return middleware.Profile{DisplayName: acc.DisplayName, AvatarURL: acc.AvatarURL}, nil
	})

	// 创建房间限流：同账号 5 次/分钟。
	room.NewHandler(roomService, logger).Register(api, roomAuth, middleware.RateLimit(5, time.Minute))

	// 信令模块：WebSocket 连接管理 + 消息转发。
	// 昵称与头像在连接建立时解析一次并缓存 —— 信令消息频率高，不能每条都查库。
	hub := signalpkg.NewHub(logger)
	signalpkg.NewHandler(hub, issuer, roomService, func(accountID int64) signalpkg.Profile {
		acc, err := accountService.Profile(context.Background(), accountID)
		if err != nil {
			return signalpkg.Profile{}
		}

		accountService.Decorate(acc)

		return signalpkg.Profile{DisplayName: acc.DisplayName, AvatarURL: acc.AvatarURL}
	}, logger).Register(api)

	// 中继模块：下发 STUN/TURN 地址与临时凭证。
	// 凭证绑定账号，因此复用带认证的中间件（不需要昵称，传 nil）。
	relayIssuer := relay.NewIssuer(cfg.TURNSharedSecret, cfg.TURNRealm, cfg.TURNHost, cfg.TURNPort, cfg.TURNTTL)
	relay.NewHandler(relayIssuer, logger).Register(api, middleware.RequireAuth(issuer, nil))

	// 战斗流量中继：NGO 的 WebSocket 中继传输经此交换战斗数据。
	// 房主是 NGO 会话中心且通常在 NAT 后 —— 直连不可达时全员经此转发。
	relayws.NewHandler(relayws.NewHub(logger), issuer, roomService, logger).Register(api)

	// 撤离结算模块：客户端上报本局战利品清单，直接落库（本期不做校验）。
	// 记录归属账号，复用带认证的中间件；上报是写库操作，限流防刷。
	extractLimit := middleware.RateLimit(10, time.Minute)
	raid.NewHandler(raid.NewRepository(pool), logger).Register(api, middleware.RequireAuth(issuer, nil), extractLimit)

	return router
}

// runRoomCleanup 后台定时清理超时房间。
//
// 每 10 分钟扫一次：清理频率远高于 2 小时的空闲阈值，
// 保证房间最多多存活 10 分钟，同时避免频繁扫库。
func runRoomCleanup(ctx context.Context, service *room.Service, logger zerolog.Logger) {
	const (
		interval = 10 * time.Minute
		idle     = 2 * time.Hour
	)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			closed, err := service.CleanupStale(ctx, idle)
			if err != nil {
				logger.Error().Err(err).Msg("清理超时房间失败")
				continue
			}

			if closed > 0 {
				logger.Info().Int64("closed", closed).Msg("已清理超时房间")
			}
		}
	}
}
