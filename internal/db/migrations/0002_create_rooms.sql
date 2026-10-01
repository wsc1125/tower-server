-- +goose Up
-- 房间表。见《Go后端设计.md》3.2.1。
--
-- room_code 是 8 位大写字母数字（去掉易混淆的 0/O/1/I/L），玩家口头分享用。
-- status 三态：waiting（等待中）/ playing（游戏中）/ closed（已关闭）。
--
-- 注意：房间**成员列表不落库** —— 成员生命周期与 WebSocket 连接绑定，
-- 存内存即可（见 3.2.4）。本表只存房间元数据。

CREATE TABLE IF NOT EXISTS rooms (
    id            BIGSERIAL PRIMARY KEY,
    room_code     VARCHAR(8)   NOT NULL UNIQUE,
    host_account  BIGINT       NOT NULL REFERENCES accounts(id),
    name          VARCHAR(64)  NOT NULL,
    max_players   SMALLINT     NOT NULL DEFAULT 4,
    is_public     BOOLEAN      NOT NULL DEFAULT true,
    status        VARCHAR(16)  NOT NULL DEFAULT 'waiting',
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    closed_at     TIMESTAMPTZ
);

-- 部分索引：只索引未关闭的房间，房间列表查询走它。
CREATE INDEX IF NOT EXISTS idx_rooms_status ON rooms (status) WHERE status != 'closed';
CREATE INDEX IF NOT EXISTS idx_rooms_code   ON rooms (room_code);

-- +goose Down
DROP INDEX IF EXISTS idx_rooms_code;
DROP INDEX IF EXISTS idx_rooms_status;
DROP TABLE IF EXISTS rooms;
