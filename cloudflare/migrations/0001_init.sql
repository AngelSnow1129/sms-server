-- SMSServer Cloudflare 版初始表结构（D1 / SQLite 方言）
-- 应用方式：wrangler d1 migrations apply DB --remote（package.json 的 deploy 脚本自动执行）

-- 待取验证码。D1 是唯一事实源（Workers 多实例间内存不共享）。
CREATE TABLE otps (
  token_hash TEXT PRIMARY KEY,       -- HMAC-SHA256(收件人号码, HMAC_SECRET) 的十六进制，与自托管版一致
  code       TEXT NOT NULL,          -- 提取出的验证码
  status     TEXT NOT NULL DEFAULT 'pending', -- pending: 待读取; read: 已读取（软删除，供对账）
  expires_at INTEGER NOT NULL,       -- 过期时间（Unix 秒）
  read_at    INTEGER,                -- 读取时间（Unix 秒），未读取为 NULL
  created_at INTEGER NOT NULL,       -- 写入时间（Unix 秒）
  updated_at INTEGER NOT NULL        -- 最近一次更新时间（Unix 秒）
);

CREATE INDEX idx_otps_expiry ON otps (expires_at); -- Cron 清理未读过期项
CREATE INDEX idx_otps_read   ON otps (read_at);    -- Cron 清理已读超时项
