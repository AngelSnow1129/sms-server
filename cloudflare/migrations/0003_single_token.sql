-- 单 token 简化：发送（webhook 路径）与读取（/api/v1/otp）共用同一个 TOKEN。
-- TOKEN 只用于鉴权、不落库，故原主键 token_hash（HMAC(手机号) 的结果）改为固定、
-- 非敏感的 slot_key：整个 Worker 只有一个取码位，其值恒为常量 "singleton"。
-- 列的主键约束随重命名保留；idx_otps_expiry / idx_otps_read 不涉及该列，无需改动。
ALTER TABLE otps RENAME COLUMN token_hash TO slot_key;
