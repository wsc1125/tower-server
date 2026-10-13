-- +goose Up
-- 撤离结算表。见《Go后端设计.md》：客户端上报本局撤离的战利品清单，
-- 服务端直接落库，本期不做防作弊校验（战利品的权威数据在客户端本地存档）。
--
-- items 存 JSONB：清单结构随客户端版本演化（可能追加耐久、词条等字段），
-- 用 JSONB 免去每次改结构的迁移；当前只做记录留存，无查询需求。

CREATE TABLE IF NOT EXISTS raid_extractions (
    id         BIGSERIAL PRIMARY KEY,
    account_id BIGINT      NOT NULL REFERENCES accounts(id),
    floor      SMALLINT    NOT NULL,
    items      JSONB       NOT NULL DEFAULT '[]',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_raid_extractions_account ON raid_extractions (account_id, created_at DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_raid_extractions_account;
DROP TABLE IF EXISTS raid_extractions;
