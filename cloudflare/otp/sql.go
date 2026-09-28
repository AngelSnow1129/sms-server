package otp

import (
	"context"
	"database/sql"
	"errors"
)

// D1 上执行的语句。以导出常量集中在此，便于独立测试模块针对真实语句文本做语义实证。
// 参数统一用 ?1/?2 编号占位（SQLite / D1 方言），同一编号可复用。
//
// 留存策略（与自托管版的关键差异，需求驱动）：
//   - 未读（pending）是关键有效数据，严禁自动删除或过期失效：
//     取码语句不再判断 expires_at（该列降级为「建议有效期」信息字段），
//     清理任务也不再删除未读记录。
//   - 已消费（read）允许累积，由每月一次的 Cron 批量物理清理（CleanupReadArchive）。
//   - 提取失败落 failed 行（code 存 FailureCode），供用户直接看到「最近一条没提到码」，
//     永不删除，由同号新短信按 upsert 覆盖。
const (
	// SQLTakeCode 取码：单条 UPDATE ... RETURNING 原子地把 pending 翻成 read 并写入
	// read_at，同时把验证码与短信原文返回给调用方。
	//
	// 这是 Cloudflare 版最关键的并发安全保证：Workers 多实例间内存不共享，
	// 若拆成 SELECT + UPDATE 两步，两个并发请求可能读到同一个验证码。
	// 单条语句由存储层保证原子性，WHERE status = 'pending' 使第二个请求
	// 匹配不到任何行，从而得到 sql.ErrNoRows（阅后即焚）。
	// WHERE 刻意不含 expires_at：未读验证码不因时间失效，只被「消费」或「同号新码覆盖」。
	// 参数：?1 = now（Unix 秒），?2 = token_hash。
	SQLTakeCode = `
UPDATE otps
SET status = 'read', read_at = ?1, updated_at = ?1
WHERE token_hash = ?2 AND status = 'pending'
RETURNING code, raw_content`

	// SQLUpsertCode 写入验证码，同号新码直接覆盖旧码（与自托管版缓存覆盖语义一致）。
	// 覆盖时 status 回到 pending 且 read_at 置 NULL：已被取走的旧码不应让新码一写入就
	// 处于已读状态；created_at 保持不变（仍是首次写入时间）。
	// raw_content 存短信原文（req.Body 原样，严禁清洗截断）。
	// 参数：?1 = token_hash，?2 = code，?3 = expires_at（信息字段），?4 = raw_content，?5 = now。
	SQLUpsertCode = `
INSERT INTO otps (token_hash, code, status, expires_at, read_at, raw_content, created_at, updated_at)
VALUES (?1, ?2, 'pending', ?3, NULL, ?4, ?5, ?5)
ON CONFLICT(token_hash) DO UPDATE SET
  code = excluded.code,
  status = 'pending',
  expires_at = excluded.expires_at,
  read_at = NULL,
  raw_content = excluded.raw_content,
  updated_at = excluded.updated_at`

	// SQLUpsertFailure 提取失败时写入失败标记：code = FailureCode，status = 'failed'。
	// 覆盖时 created_at 同样保持不变；expires_at 对 failed 行无意义，沿用 now。
	// 参数：?1 = token_hash，?2 = FailureCode，?3 = raw_content，?4 = now。
	SQLUpsertFailure = `
INSERT INTO otps (token_hash, code, status, expires_at, read_at, raw_content, created_at, updated_at)
VALUES (?1, ?2, 'failed', ?4, NULL, ?3, ?4, ?4)
ON CONFLICT(token_hash) DO UPDATE SET
  code = excluded.code,
  status = 'failed',
  read_at = NULL,
  raw_content = excluded.raw_content,
  updated_at = excluded.updated_at`

	// SQLDeleteReadArchive 每月一次的已消费记录批量物理清理：只删 status='read'
	// 且 read_at 超出 ReadArchiveWindowSeconds 窗口的行（read_at 为 NULL 时不删，
	// 防御性条件）。未读（pending）与失败（failed）记录严禁自动删除，不在本语句范围内。
	// 参数：?1 = now - 窗口秒数。
	SQLDeleteReadArchive = `DELETE FROM otps WHERE status = 'read' AND read_at IS NOT NULL AND read_at < ?1`

	// SQLLookupState 查询某 token 当前行的状态。取码语句（WHERE status='pending'）
	// 命中不到 failed 行，需要这条独立 SELECT 在取码未命中时区分
	// 「从未写入 / 已消费(read) / 提取失败(failed)」，让失败原因对用户可见。
	// 参数：?1 = token_hash。
	SQLLookupState = `SELECT code, status, raw_content FROM otps WHERE token_hash = ?1`
)

// DB 抽象 D1（*sql.DB）上用到的方法。
// 仅依赖标准库，便于测试替身，也保证本包可在 linux/amd64 下编译。
type DB interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// TakenOTP 一次成功取码的结果：验证码 + 完整短信原文。
type TakenOTP struct {
	Code       string
	RawContent string
}

// TakeCode 原子地把 pending 记录翻成 read 并返回验证码与短信原文。
// 未写入 / 已被取走时返回 sql.ErrNoRows（调用方按 pending 处理）。
// 注意：不再按 expires_at 过滤——未读验证码严禁过期失效（见 SQLTakeCode 注释）。
// now 为当前 Unix 秒，由调用方传入以便测试固定时间。
func TakeCode(ctx context.Context, db DB, token string, now int64) (TakenOTP, error) {
	var r TakenOTP
	err := db.QueryRowContext(ctx, SQLTakeCode, now, token).Scan(&r.Code, &r.RawContent)
	if err != nil {
		return TakenOTP{}, err
	}
	return r, nil
}

// SaveOTP 写入验证码（upsert）。rawContent 为短信原文，必须原样传入。
// now 为当前 Unix 秒；expireAt 仅作为信息字段写入（建议有效期），不参与取码与清理判断。
func SaveOTP(ctx context.Context, db DB, tokenHash, code, rawContent string, now, expireAt int64) error {
	_, err := db.ExecContext(ctx, SQLUpsertCode, tokenHash, code, expireAt, rawContent, now)
	return err
}

// SaveFailure 写入提取失败标记（upsert）：用户之后取码即可看到 FailureCode 与原文，
// 而不是面对一个无法区分的 pending。该记录不会被清理任务删除，同号新短信到达时覆盖。
func SaveFailure(ctx context.Context, db DB, tokenHash, rawContent string, now int64) error {
	_, err := db.ExecContext(ctx, SQLUpsertFailure, tokenHash, FailureCode, rawContent, now)
	return err
}

// CleanupReadArchive 每月一次的批量物理清理（Cron Trigger，见 wrangler.jsonc）：
// 只删除已消费（read）且超出 ReadArchiveWindowSeconds 窗口的记录。
// 未读（pending）与失败（failed）记录属于严禁自动删除的数据，本函数不触碰。
// 返回删除的行数。now 为当前 Unix 秒。
func CleanupReadArchive(ctx context.Context, db DB, now int64) (deleted int64, err error) {
	res, err := db.ExecContext(ctx, SQLDeleteReadArchive, now-ReadArchiveWindowSeconds)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// OTPState 某 token 当前行的状态（取码未命中时用于区分失败原因）。
type OTPState struct {
	Found      bool
	Code       string
	Status     string // pending / read / failed
	RawContent string
}

// LookupState 查询 token 对应行的当前状态；行不存在时 Found=false（无错误）。
func LookupState(ctx context.Context, db DB, token string) (OTPState, error) {
	var s OTPState
	err := db.QueryRowContext(ctx, SQLLookupState, token).Scan(&s.Code, &s.Status, &s.RawContent)
	if errors.Is(err, sql.ErrNoRows) {
		return OTPState{}, nil
	}
	if err != nil {
		return OTPState{}, err
	}
	s.Found = true
	return s, nil
}
