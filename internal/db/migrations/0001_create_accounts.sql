-- +goose Up
-- 账号表。见《Go后端设计.md》3.1.1。
--
-- username 唯一，登录用；display_name 可重复，游戏内显示用。
-- password_hash 存 bcrypt 输出（固定 60 字符），永不存明文。
-- 本期不做邮箱 / 找回密码（无邮件服务依赖）。

CREATE TABLE IF NOT EXISTS accounts (
    id            BIGSERIAL PRIMARY KEY,
    username      VARCHAR(32)  NOT NULL UNIQUE,
    password_hash VARCHAR(72)  NOT NULL,
    display_name  VARCHAR(32)  NOT NULL,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    last_login_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_accounts_username ON accounts (username);

-- +goose Down
DROP INDEX IF EXISTS idx_accounts_username;
DROP TABLE IF EXISTS accounts;
