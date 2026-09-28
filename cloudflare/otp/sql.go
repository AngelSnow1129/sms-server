package otp

import (
	"context"
	"database/sql"
)

// D1 上执行的语句。以导出常量集中在此，便于独立测试模块针对真实语句文本做语义实证。
// 参数统一用 ?1/?2 编号占位（SQLite / D1 方言），同一编号可复用。
const (
	// SQLTakeCode 取码：单条 UPDATE ... RETURNING 原子地把 pending 翻成 read 并写入
	// read_at，同时把验证码返回给调用方。
	//
	// 这是 Cloudflare 版最关键的并发安全保证：Workers 多实例间内存不共享，
	// 若拆成 SELECT + UPDATE 两步，两个并发请求可能读到同一个验证码。
	// 单条语句由存储层保证原子性，WHERE status = 'pending' 使第二个请求
	// 匹配不到任何行，从而得到 sql.ErrNoRows（阅后即焚）。
	// 参数：?1 = now（Unix 秒），?2 = token_hash。
	SQLTakeCode = `
UPDATE otps
SET status = 'read', read_at = ?1, updated_at = ?1
WHERE token_hash = ?2 AND status = 'pending' AND expires_at > ?1
RETURNING code`

	// SQLUpsertCode 写入验证码，同号新码直接覆盖旧码（与自托管版缓存覆盖语义一致）。
	// 覆盖时 status 回到 pending 且 read_at 置 NULL：已被取走的旧码不应让新码一写入就
	// 处于已读状态；created_at 保持不变（仍是首次写入时间）。
	// 参数：?1 = token_hash，?2 = code，?3 = expires_at，?4 = now。
	SQLUpsertCode = `
INSERT INTO otps (token_hash, code, status, expires_at, read_at, created_at, updated_at)
VALUES (?1, ?2, 'pending', ?3, NULL, ?4, ?4)
ON CONFLICT(token_hash) DO UPDATE SET
  code = excluded.code,
  status = 'pending',
  expires_at = excluded.expires_at,
  read_at = NULL,
  updated_at = excluded.updated_at`

	// SQLDeleteReadExpired 清理已读且超过保留窗口的记录（read_at 为 NULL 时不删，
	// 防御性条件，避免异常数据被提前清掉）。参数：?1 = now - 保留秒数。
	SQLDeleteReadExpired = `DELETE FROM otps WHERE status = 'read' AND read_at IS NOT NULL AND read_at < ?1`

	// SQLDeleteUnreadExpired 清理未读但已过期的记录。参数：?1 = now。
	SQLDeleteUnreadExpired = `DELETE FROM otps WHERE status = 'pending' AND expires_at < ?1`
)

// DB 抽象 D1（*sql.DB）上用到的方法。
// 仅依赖标准库，便于测试替身，也保证本包可在 linux/amd64 下编译。
type DB interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// TakeCode 原子地把 pending 记录翻成 read 并返回验证码。
// 未写入 / 已过期 / 已被取走时返回 sql.ErrNoRows（调用方按 pending 处理）。
// now 为当前 Unix 秒，由调用方传入以便测试固定时间。
func TakeCode(ctx context.Context, db DB, token string, now int64) (string, error) {
	var code string
	err := db.QueryRowContext(ctx, SQLTakeCode, now, token).Scan(&code)
	if err != nil {
		return "", err
	}
	return code, nil
}

// SaveOTP 写入验证码（upsert）。now 为当前 Unix 秒，expireAt 为过期 Unix 秒。
func SaveOTP(ctx context.Context, db DB, tokenHash, code string, now, expireAt int64) error {
	_, err := db.ExecContext(ctx, SQLUpsertCode, tokenHash, code, expireAt, now)
	return err
}

// CleanupExpired Cron 清理任务（每 30 分钟，见 wrangler.jsonc）：
// 删除已读超过 ReadRetentionSeconds 的记录与未读但已过期的记录，
// 返回两条语句各自删除的行数。now 为当前 Unix 秒。
func CleanupExpired(ctx context.Context, db DB, now int64) (readDeleted, expiredDeleted int64, err error) {
	res, err := db.ExecContext(ctx, SQLDeleteReadExpired, now-ReadRetentionSeconds)
	if err != nil {
		return 0, 0, err
	}
	readDeleted, _ = res.RowsAffected()

	res, err = db.ExecContext(ctx, SQLDeleteUnreadExpired, now)
	if err != nil {
		return 0, 0, err
	}
	expiredDeleted, _ = res.RowsAffected()
	return readDeleted, expiredDeleted, nil
}
