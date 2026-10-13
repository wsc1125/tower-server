// Package raid 撤离结算模块：客户端上报本局撤离的战利品清单。
//
// 职责边界：只做「记录留存」—— 客户端本地已结算（局内携带并入仓库），
// 上报失败也不阻塞本地流程。因此这里不做防作弊校验，
// 只保证请求格式合法、落库成功。
package raid

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ExtractItem 单件战利品。字段与客户端 ExtractionItemDto 一一对应。
type ExtractItem struct {
	DefinitionID string `json:"definition_id"`
	Durability   int    `json:"durability"`
}

// Repository 撤离结算仓储。
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository 创建撤离结算仓储。
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Insert 落库一条撤离记录。items 允许为 nil（空局也值得记录层数）。
func (r *Repository) Insert(ctx context.Context, accountID int64, floor int, items []ExtractItem) error {
	if items == nil {
		items = []ExtractItem{}
	}

	payload, err := json.Marshal(items)
	if err != nil {
		return err
	}

	_, err = r.pool.Exec(ctx, `
		INSERT INTO raid_extractions (account_id, floor, items)
		VALUES ($1, $2, $3::jsonb)`,
		accountID, floor, payload)

	return err
}
