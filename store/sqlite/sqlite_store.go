// Package sqlite 提供基于 SQLite 的 store.Store 实现，驱动为 modernc.org/sqlite（纯 Go，无 CGO）。
//
// 为什么不用 gorm.io/driver/sqlite：它底层是 mattn/go-sqlite3，需要 CGO，
// 与本项目 CGO_ENABLED=0 + distroless/static 的构建链路不兼容（见 Dockerfile）。
//
// 与内存实现 cache.OTPCache 的语义差异只有一处：读取成功后记录不物理删除，
// 而是把 status 从 pending 翻成 read（软删除），保留 readRetentionSeconds 供对账，
// 由 Cleanup 统一物理清理——这与 cloudflare 版（D1）的行为一致。
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	"smsserver/store"

	_ "modernc.org/sqlite" // 注册 database/sql 的 sqlite 驱动（纯 Go，无需 CGO）
)

// 编译期断言：Store 必须满足 store.Store，
// 否则 main.go 接线处的类型错误要到运行期才暴露。
var _ store.Store = (*Store)(nil)

// statusPending / statusRead 与 D1 表 otps.status 的取值一致：
// pending 待读取，read 已读取（软删除，供对账）。
const (
	statusPending = "pending"
	statusRead    = "read"
)

// readRetentionSeconds 已读记录的保留时长，超过后由 Cleanup 物理删除。
// 与 cloudflare/main.go 的 readRetentionSeconds 取值一致，保留窗口用于对账
// 「调用方说没收到码」，避免阅后即焚造成的诊断盲区。
const readRetentionSeconds = 600 // 10 分钟

// tokenLogPrefixLen 日志中记录的 token 前缀长度，与 service 保持一致。
// 完整 token 等同缓存 key，验证码与手机号明文同样禁止入日志，详见 docs/LOGGING.md。
const tokenLogPrefixLen = 8

// 建表语句与 cloudflare/migrations/0001_init.sql（D1）逐列对齐：同表名、同列名、
// 同列序、同索引名，仅把两个索引补成 IF NOT EXISTS（SQLite 支持，MySQL 不支持）。
//
// 时间列单位：D1 版存 Unix **秒**，本实现存 Unix **毫秒**。
// 差异的原因是 store.Store 的契约用例以毫秒级 TTL（30ms/40ms）验证过期语义，
// 秒级整数无法表达「写入后 40ms 过期」——int64(40ms/1s) 恒为 0，会让新写入的
// 记录在 100% 的相位下立即被判为过期（expires_at == floor(now)，不满足 > now）。
// 毫秒与秒同为 INTEGER Unix 时间戳，列结构不变；两个库各自独立读写，不存在跨库解析。
const schema = `
CREATE TABLE IF NOT EXISTS otps (
  token_hash TEXT PRIMARY KEY,
  code       TEXT NOT NULL,
  status     TEXT NOT NULL DEFAULT 'pending',
  expires_at INTEGER NOT NULL,
  read_at    INTEGER,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_otps_expiry ON otps (expires_at);
CREATE INDEX IF NOT EXISTS idx_otps_read   ON otps (read_at);
`

// Store 基于 SQLite 的验证码存储
type Store struct {
	db  *sql.DB
	ttl time.Duration
}

// New 打开 path 处的 SQLite 库（不存在则创建），建表并返回一个 Store。
// ttl 决定 Set 写入的 expires_at。构造函数可以返回 error，
// 但 store.Store 的方法签名被上层锁死不带 error，失败只能在内部记日志。
//
// 连接串刻意不使用 :memory:：内存库是每个连接各一份，database/sql 的连接池会让
// 写入落到与读取不同的库上。测试请用临时文件（t.TempDir()），生产用 SQLITE_PATH。
func New(path string, ttl time.Duration) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}

	// SQLite 是单写者模型：并发写会报 SQLITE_BUSY。把连接池压到 1 个连接，
	// 由驱动侧串行化写请求，避免业务层拿到 "database is locked"；
	// 读请求复用同一连接，不存在跨连接可见性问题。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("打开 SQLite 失败 path=%s: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("初始化 SQLite 表结构失败: %w", err)
	}
	return &Store{db: db, ttl: ttl}, nil
}

// Close 关闭底层数据库连接，供优雅关闭时调用以确保写盘完成。
func (s *Store) Close() error {
	return s.db.Close()
}

// Set 写入或覆盖验证码：同一 token 重复写入时新码覆盖旧码，旧码立即失效。
// 覆盖一条已读记录时把它翻回 pending 并清空 read_at，与内存实现「覆盖即新码」一致。
// 失败只记录日志：验证码写不进去会影响后续取码，但不应让整条短信处理链路失败。
func (s *Store) Set(token, code string) {
	now := nowMillis()
	expires := now + s.ttl.Milliseconds()

	// UPSERT：冲突时覆盖 code/status/expires_at/updated_at 并清空 read_at
	const q = `
INSERT INTO otps (token_hash, code, status, expires_at, read_at, created_at, updated_at)
VALUES (?1, ?2, '` + statusPending + `', ?3, NULL, ?4, ?4)
ON CONFLICT(token_hash) DO UPDATE SET
  code       = excluded.code,
  status     = '` + statusPending + `',
  expires_at = excluded.expires_at,
  read_at    = NULL,
  updated_at = excluded.updated_at`

	if _, err := s.db.ExecContext(context.Background(), q, token, code, expires, now); err != nil {
		// 只记录 token 前 8 位，绝不记录验证码明文
		log.Printf("[验证码存储] 写入失败 token_prefix=%s err=%v", logToken(token), err)
	}
}

// Get 读取验证码但不使其失效；token 不存在、已过期或已被读取过时返回 ok=false。
// 查询出错同样按未命中返回：调用方无法区分「不存在」与「出错」，
// 统一回落 ok=false 与内存实现的语义一致。
func (s *Store) Get(token string) (string, bool) {
	const q = `
SELECT code FROM otps
WHERE token_hash = ?1 AND status = '` + statusPending + `' AND expires_at > ?2`

	var code string
	err := s.db.QueryRowContext(context.Background(), q, token, nowMillis()).Scan(&code)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false
	case err != nil:
		log.Printf("[验证码存储] 读取失败 token_prefix=%s err=%v", logToken(token), err)
		return "", false
	}
	return code, true
}

// Delete 删除指定验证码；token 不存在时无副作用。
// 这里是物理删除，与 GetAndDelete 的软删除不同：调用方明确要求移除该条目。
func (s *Store) Delete(token string) {
	if _, err := s.db.ExecContext(context.Background(),
		`DELETE FROM otps WHERE token_hash = ?1`, token); err != nil {
		log.Printf("[验证码存储] 删除失败 token_prefix=%s err=%v", logToken(token), err)
	}
}

// GetAndDelete 原子地读取验证码并使其失效（阅后即焚）。
//
// 单条 UPDATE ... RETURNING：先匹配 pending 且未过期的行，命中即在同一次原子写里
// 翻成 read 并写入 read_at，再把 code 返回。并发下只有一个调用方能匹配到该行，
// 其余调用方拿到 sql.ErrNoRows，从而保证同一条验证码只会被取走一次。
// 禁止拆成 SELECT 再 UPDATE —— 两步之间存在窗口，会让同一条码被取走两次。
func (s *Store) GetAndDelete(token string) (string, bool) {
	const q = `
UPDATE otps SET status = '` + statusRead + `', read_at = ?1, updated_at = ?1
WHERE token_hash = ?2 AND status = '` + statusPending + `' AND expires_at > ?1
RETURNING code`

	var code string
	err := s.db.QueryRowContext(context.Background(), q, nowMillis(), token).Scan(&code)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false
	case err != nil:
		log.Printf("[验证码存储] 取码失败 token_prefix=%s err=%v", logToken(token), err)
		return "", false
	}
	return code, true
}

// Cleanup 物理清理两类记录，返回清理条数：
//  1. 已读且读取时间超过 readRetentionSeconds 的记录（软删除的收尾）；
//  2. 未读但已过期的记录。
//
// 与 cloudflare/main.go 的 cleanupExpired 行为一致。失败返回 0：
// 清理协程只按返回值打日志，签名锁死没有错误上抛通道。
func (s *Store) Cleanup() int {
	now := nowMillis()
	ctx := context.Background()
	removed := 0

	res, err := s.db.ExecContext(ctx,
		`DELETE FROM otps WHERE status = '`+statusRead+`' AND read_at IS NOT NULL AND read_at < ?1`,
		now-readRetentionSeconds*1000)
	if err != nil {
		log.Printf("[验证码存储] 清理已读记录失败 err=%v", err)
		return 0
	}
	if n, err := res.RowsAffected(); err == nil {
		removed += int(n)
	}

	res, err = s.db.ExecContext(ctx,
		`DELETE FROM otps WHERE status = '`+statusPending+`' AND expires_at < ?1`, now)
	if err != nil {
		log.Printf("[验证码存储] 清理过期记录失败 err=%v", err)
		return removed
	}
	if n, err := res.RowsAffected(); err == nil {
		removed += int(n)
	}
	return removed
}

// Len 返回当前未失效的条目数：pending 且未过期，正好是 Cleanup 之后 Get 仍可读的集合。
// 已读记录不计入（Get 读不到），与内存实现「取走即减一」的语义对齐。
func (s *Store) Len() int {
	const q = `
SELECT COUNT(*) FROM otps
WHERE status = '` + statusPending + `' AND expires_at > ?1`

	var n int
	if err := s.db.QueryRowContext(context.Background(), q, nowMillis()).Scan(&n); err != nil {
		log.Printf("[验证码存储] 统计失败 err=%v", err)
		return 0
	}
	return n
}

// nowMillis 当前时间的 Unix 毫秒，与 schema 注释里的时间列单位一致。
func nowMillis() int64 {
	return time.Now().UnixMilli()
}

// logToken 把 token 截断到日志允许的前缀长度。
// token 是手机号 HMAC-SHA256 的十六进制串，完整值等同缓存 key，不得入日志。
func logToken(token string) string {
	if len(token) > tokenLogPrefixLen {
		return token[:tokenLogPrefixLen]
	}
	return token
}
