// Package assets 玩家资产云存档模块：整包快照的存取。
//
// 职责边界：只做「快照留存」—— 第一期客户端权威，服务端把 data 当不透明 JSON
// 存取，不做游戏逻辑校验。接口形状与数据结构为后续服务器权威化预留升级空间
// （revision → If-Match 乐观锁 → 撤离/死亡改服务端事务）。
package assets

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Snapshot 一条资产快照记录。
type Snapshot struct {
	SchemaVersion int
	Revision      int64
	UpdatedAt     string
	Data          json.RawMessage
}

// Repository 资产快照仓储。
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository 创建资产快照仓储。
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Get 读取账号快照。账号尚无快照时返回 (nil, nil) —— 由 handler 转成 revision=0 响应。
func (r *Repository) Get(ctx context.Context, accountID int64) (*Snapshot, error) {
	var (
		schemaVersion int
		revision      int64
		updatedAt     string
		data          []byte
	)

	err := r.pool.QueryRow(ctx, `
		SELECT schema_version, revision, updated_at::text, data
		FROM player_assets
		WHERE account_id = $1`,
		accountID).Scan(&schemaVersion, &revision, &updatedAt, &data)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	return &Snapshot{
		SchemaVersion: schemaVersion,
		Revision:      revision,
		UpdatedAt:     updatedAt,
		Data:          data,
	}, nil
}

// Upsert 写入账号快照并返回新的 revision。
//
// 首次写入 revision = 1，之后每次 +1。整包覆盖是幂等的：
// 客户端断网重试重复提交同一份快照不会产生副作用。
func (r *Repository) Upsert(ctx context.Context, accountID int64, schemaVersion int, data json.RawMessage) (int64, error) {
	var revision int64

	err := r.pool.QueryRow(ctx, `
		INSERT INTO player_assets (account_id, schema_version, data, revision)
		VALUES ($1, $2, $3::jsonb, 1)
		ON CONFLICT (account_id) DO UPDATE SET
			schema_version = EXCLUDED.schema_version,
			data           = EXCLUDED.data,
			revision       = player_assets.revision + 1,
			updated_at     = now()
		RETURNING revision`,
		accountID, schemaVersion, data).Scan(&revision)

	if err != nil {
		return 0, err
	}

	return revision, nil
}
