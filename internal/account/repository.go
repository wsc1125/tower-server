// Package account 账号模块：数据模型与数据访问。
package account

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrUsernameTaken 用户名已被占用。
var ErrUsernameTaken = errors.New("用户名已被占用")

// ErrNotFound 账号不存在。
var ErrNotFound = errors.New("账号不存在")

// Account 账号实体。对应 accounts 表。
//
// PasswordHash 不参与 JSON 序列化 —— 它绝不该出现在任何响应里。
type Account struct {
	ID           int64      `json:"account_id"`
	Username     string     `json:"username"`
	PasswordHash string     `json:"-"`
	DisplayName  string     `json:"display_name"`
	AvatarPath   string     `json:"-"`
	AvatarURL    string     `json:"avatar_url,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	LastLoginAt  *time.Time `json:"last_login_at,omitempty"`
}

// Repository 账号数据访问。手写 pgx 查询，不引入 ORM / 代码生成。
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository 创建账号仓储。
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Create 插入新账号，返回带 ID 的完整实体。
//
// 用户名冲突靠数据库唯一约束判定（而非先查后插）：
// 先查后插在并发下有竞态，两个请求可能同时通过检查。
func (r *Repository) Create(ctx context.Context, username, passwordHash, displayName, avatarPath string) (*Account, error) {
	const query = `
		INSERT INTO accounts (username, password_hash, display_name, avatar_path)
		VALUES ($1, $2, $3, NULLIF($4, ''))
		RETURNING id, username, password_hash, display_name, COALESCE(avatar_path, ''), created_at, updated_at, last_login_at`

	var acc Account
	err := r.pool.QueryRow(ctx, query, username, passwordHash, displayName, avatarPath).Scan(
		&acc.ID, &acc.Username, &acc.PasswordHash, &acc.DisplayName, &acc.AvatarPath,
		&acc.CreatedAt, &acc.UpdatedAt, &acc.LastLoginAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrUsernameTaken
		}

		return nil, fmt.Errorf("插入账号失败: %w", err)
	}

	return &acc, nil
}

// FindByUsername 按用户名查账号。不存在时返回 ErrNotFound。
func (r *Repository) FindByUsername(ctx context.Context, username string) (*Account, error) {
	const query = `
		SELECT id, username, password_hash, display_name, COALESCE(avatar_path, ''), created_at, updated_at, last_login_at
		FROM accounts
		WHERE username = $1`

	var acc Account
	err := r.pool.QueryRow(ctx, query, username).Scan(
		&acc.ID, &acc.Username, &acc.PasswordHash, &acc.DisplayName, &acc.AvatarPath,
		&acc.CreatedAt, &acc.UpdatedAt, &acc.LastLoginAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}

		return nil, fmt.Errorf("查询账号失败: %w", err)
	}

	return &acc, nil
}

// FindByID 按 ID 查账号。不存在时返回 ErrNotFound。
func (r *Repository) FindByID(ctx context.Context, id int64) (*Account, error) {
	const query = `
		SELECT id, username, password_hash, display_name, COALESCE(avatar_path, ''), created_at, updated_at, last_login_at
		FROM accounts
		WHERE id = $1`

	var acc Account
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&acc.ID, &acc.Username, &acc.PasswordHash, &acc.DisplayName, &acc.AvatarPath,
		&acc.CreatedAt, &acc.UpdatedAt, &acc.LastLoginAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}

		return nil, fmt.Errorf("查询账号失败: %w", err)
	}

	return &acc, nil
}

// UpdateAvatar 写入头像相对路径。空串表示清空头像。
func (r *Repository) UpdateAvatar(ctx context.Context, id int64, avatarPath string) error {
	const query = `UPDATE accounts SET avatar_path = NULLIF($2, ''), updated_at = now() WHERE id = $1`

	if _, err := r.pool.Exec(ctx, query, id, avatarPath); err != nil {
		return fmt.Errorf("更新头像失败: %w", err)
	}

	return nil
}

// TouchLastLogin 记录本次登录时间。失败不影响登录流程，由调用方决定是否忽略。
func (r *Repository) TouchLastLogin(ctx context.Context, id int64) error {
	const query = `UPDATE accounts SET last_login_at = now(), updated_at = now() WHERE id = $1`

	if _, err := r.pool.Exec(ctx, query, id); err != nil {
		return fmt.Errorf("更新登录时间失败: %w", err)
	}

	return nil
}

// isUniqueViolation 判断是否违反唯一约束（PostgreSQL 错误码 23505）。
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}

	return false
}
