package db

import (
	"context"
	"embed"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// migrationsFS 把迁移 SQL 嵌进二进制。
//
// 嵌入而非读磁盘：部署时只需拷一个可执行文件，不必连带 migrations 目录，
// 也避免「工作目录不对导致找不到迁移文件」这类部署事故。
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate 执行全部未应用的迁移。
//
// 用 goose 的库模式而非 CLI：服务启动时自动建表，本地开发少一步操作，
// 部署时也不会出现「忘了跑迁移」导致表不存在。
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	// goose 需要 database/sql 接口，这里用 pgx 的 stdlib 适配器包一层。
	// 复用同一个连接串，因此与 pgxpool 指向同一个库。
	connConfig := pool.Config().ConnConfig
	sqlDB := stdlib.OpenDB(*connConfig)
	defer sqlDB.Close()

	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("设置 goose 方言失败: %w", err)
	}

	if err := goose.UpContext(ctx, sqlDB, "migrations"); err != nil {
		return fmt.Errorf("执行数据库迁移失败: %w", err)
	}

	return nil
}
