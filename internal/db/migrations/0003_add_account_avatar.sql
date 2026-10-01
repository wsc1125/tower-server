-- +goose Up
-- 账号头像。见《Go后端设计.md》3.1.1。
--
-- avatar_path 存的是「相对头像根目录的路径」（如 "42/9f3c1a.jpg"），
-- 不是完整 URL：域名会随部署环境变化，存 URL 会让换域名变成一次数据迁移。
-- 客户端拿到的 avatar_url 由服务端按当前配置拼出来。
--
-- 允许为空：老账号没有头像，注册时也可以不传。

ALTER TABLE accounts ADD COLUMN IF NOT EXISTS avatar_path VARCHAR(255);

-- +goose Down
ALTER TABLE accounts DROP COLUMN IF EXISTS avatar_path;
