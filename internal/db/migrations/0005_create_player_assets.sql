-- +goose Up
-- 玩家资产快照表。见《玩家资产云存档设计方案》：
-- 仓库/装备 + 收藏 + 星空币合并为一个 JSON 快照，整包原子上下传。
--
-- 每账号一行（account_id 主键）：GET/PUT 天然原子，一个接口搞定；
-- 拆表就要拆接口、拆事务，而当前无逐件查询需求。
--
-- data 存 JSONB：快照结构随客户端版本演化（信封内层是客户端存档 v3 结构），
-- 服务端只透传不解析，用 JSONB 免去每次改结构的迁移。
--
-- revision 服务端自增：现在用于排查「谁覆盖了谁」，
-- 未来直接升级为 If-Match 乐观锁，不用改表。

CREATE TABLE IF NOT EXISTS player_assets (
    account_id     BIGINT      PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    schema_version INT         NOT NULL DEFAULT 1,
    data           JSONB       NOT NULL,
    revision       BIGINT      NOT NULL DEFAULT 1,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS player_assets;
