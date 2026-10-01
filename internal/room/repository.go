package room

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrCodeTaken 房间码已被占用（生成时冲突）。
var ErrCodeTaken = errors.New("房间码已被占用")

// ErrRoomNotFound 房间不存在。
var ErrRoomNotFound = errors.New("房间不存在")

// Record 房间的数据库记录。
//
// 与内存 Room 分开：数据库只存「房间元信息 + 房主归属」，
// 成员列表是运行时状态，不落库。
type Record struct {
	ID          int64      `json:"-"`
	Code        string     `json:"room_code"`
	HostAccount int64      `json:"host_account"`
	Name        string     `json:"name"`
	MaxPlayers  int        `json:"max_players"`
	IsPublic    bool       `json:"is_public"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ClosedAt    *time.Time `json:"closed_at,omitempty"`
}

// Repository 房间数据访问。
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository 创建房间仓储。
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Create 落库新房间。房间码冲突时返回 ErrCodeTaken。
func (r *Repository) Create(ctx context.Context, code string, hostAccount int64, name string, maxPlayers int, isPublic bool) (*Record, error) {
	const query = `
		INSERT INTO rooms (room_code, host_account, name, max_players, is_public, status)
		VALUES ($1, $2, $3, $4, $5, 'waiting')
		RETURNING id, room_code, host_account, name, max_players, is_public, status, created_at, updated_at, closed_at`

	var rec Record
	err := r.pool.QueryRow(ctx, query, code, hostAccount, name, maxPlayers, isPublic).Scan(
		&rec.ID, &rec.Code, &rec.HostAccount, &rec.Name, &rec.MaxPlayers,
		&rec.IsPublic, &rec.Status, &rec.CreatedAt, &rec.UpdatedAt, &rec.ClosedAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrCodeTaken
		}

		return nil, fmt.Errorf("插入房间失败: %w", err)
	}

	return &rec, nil
}

// FindByCode 按房间码查房间。不存在时返回 ErrRoomNotFound。
func (r *Repository) FindByCode(ctx context.Context, code string) (*Record, error) {
	const query = `
		SELECT id, room_code, host_account, name, max_players, is_public, status, created_at, updated_at, closed_at
		FROM rooms
		WHERE room_code = $1`

	var rec Record
	err := r.pool.QueryRow(ctx, query, code).Scan(
		&rec.ID, &rec.Code, &rec.HostAccount, &rec.Name, &rec.MaxPlayers,
		&rec.IsPublic, &rec.Status, &rec.CreatedAt, &rec.UpdatedAt, &rec.ClosedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrRoomNotFound
		}

		return nil, fmt.Errorf("查询房间失败: %w", err)
	}

	return &rec, nil
}

// ListPublicWaiting 列出公开且等待中的房间，附带房主昵称与当前人数。
//
// 当前人数来自内存注册表（成员不落库），因此这里只查房间元信息，
// 人数由 service 层从注册表补齐。
func (r *Repository) ListPublicWaiting(ctx context.Context) ([]*Record, error) {
	const query = `
		SELECT id, room_code, host_account, name, max_players, is_public, status, created_at, updated_at, closed_at
		FROM rooms
		WHERE status = 'waiting' AND is_public = true
		ORDER BY created_at DESC
		LIMIT 100`

	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("查询房间列表失败: %w", err)
	}
	defer rows.Close()

	var records []*Record
	for rows.Next() {
		var rec Record
		if err := rows.Scan(
			&rec.ID, &rec.Code, &rec.HostAccount, &rec.Name, &rec.MaxPlayers,
			&rec.IsPublic, &rec.Status, &rec.CreatedAt, &rec.UpdatedAt, &rec.ClosedAt,
		); err != nil {
			return nil, fmt.Errorf("扫描房间行失败: %w", err)
		}

		records = append(records, &rec)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历房间列表失败: %w", err)
	}

	return records, nil
}

// UpdateHost 更新房主（房主迁移时调用）。
func (r *Repository) UpdateHost(ctx context.Context, code string, newHost int64) error {
	const query = `UPDATE rooms SET host_account = $2, updated_at = now() WHERE room_code = $1`

	if _, err := r.pool.Exec(ctx, query, code, newHost); err != nil {
		return fmt.Errorf("更新房主失败: %w", err)
	}

	return nil
}

// UpdateStatus 更新房间状态。
func (r *Repository) UpdateStatus(ctx context.Context, code string, status Status) error {
	const query = `UPDATE rooms SET status = $2, updated_at = now() WHERE room_code = $1`

	if _, err := r.pool.Exec(ctx, query, code, string(status)); err != nil {
		return fmt.Errorf("更新房间状态失败: %w", err)
	}

	return nil
}

// Close 关闭房间：置 status=closed 并记录关闭时间。
func (r *Repository) Close(ctx context.Context, code string) error {
	const query = `
		UPDATE rooms
		SET status = 'closed', closed_at = now(), updated_at = now()
		WHERE room_code = $1 AND status != 'closed'`

	if _, err := r.pool.Exec(ctx, query, code); err != nil {
		return fmt.Errorf("关闭房间失败: %w", err)
	}

	return nil
}

// CloseStale 关闭超时未活动的房间，返回关闭数量。
//
// 只关 waiting 状态的房间：playing 中的房间可能长时间没有 HTTP 活动
// （玩家在游戏里），按「无活动」判定会误杀进行中的对局。
func (r *Repository) CloseStale(ctx context.Context, idle time.Duration) (int64, error) {
	const query = `
		UPDATE rooms
		SET status = 'closed', closed_at = now(), updated_at = now()
		WHERE status = 'waiting' AND updated_at < now() - $1::interval`

	tag, err := r.pool.Exec(ctx, query, idle.String())
	if err != nil {
		return 0, fmt.Errorf("清理超时房间失败: %w", err)
	}

	return tag.RowsAffected(), nil
}

// isUniqueViolation 判断是否违反唯一约束（PostgreSQL 错误码 23505）。
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}

	return false
}
