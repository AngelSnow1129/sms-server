// Package mysql 提供基于 MySQL 的 store.Store 实现，复用 main.go 已建立的 *gorm.DB。
//
// 表结构、语义与 store/sqlite 逐条对齐（同一套 store.Store 契约用例覆盖），
// 也和 cloudflare 版（D1）的 otps 表一致，差别只有两处，都是方言导致的：
//
//  1. 时间列单位：本实现与 D1 一致存 Unix **秒**（int64）。
//     SQLite 版存毫秒是已知且已记录的偏差——它为了让契约用例里 30ms/40ms 级别的
//     TTL 能表达过期；秒级整数无法表达「写入后 40ms 过期」（int64(40ms/1s)==0，
//     会让新写入的记录在任何相位下都立即判为过期）。MySQL 版服务真实 TTL 是分钟级，
//     不需要迁就毫秒级契约，因此按 D1 的秒级语义落地，契约测试里依赖毫秒 TTL 的
//     用例对本实现跳过（见 store_test.go 的 secondPrecision）。
//  2. 取码：MySQL 不支持 UPDATE ... RETURNING（那是 SQLite/PostgreSQL 的语法），
//     改用「条件 UPDATE 抢占 + 按 RowsAffected 判定 + SELECT 取回」的实现，
//     抢占条件与 SQLite 版 RETURNING 的 WHERE 完全一致，见 GetAndDelete。
//
// 建表方式：GORM AutoMigrate + 模型上的 gorm index 标签，与 model.SMSRecord 的做法
// 一致。不手写 CREATE INDEX —— MySQL 不支持 CREATE INDEX IF NOT EXISTS，
// 手写会在每次启动时报 Error 1064（见 docs/LOGGING.md 8.7）。
package mysql

import (
	"errors"
	"log"
	"time"

	"smsserver/store"

	"gorm.io/gorm"
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

// tokenLogPrefixLen 日志中记录的 token 前缀长度，与 service、store/sqlite 一致。
// 完整 token 等同缓存 key，验证码与手机号明文同样禁止入日志，详见 docs/LOGGING.md。
const tokenLogPrefixLen = 8

// otpRecord 是 otps 表的 GORM 模型，**只用于 AutoMigrate 建表与建索引**，
// 不参与任何读写：读写全部走本文件下面的原生 SQL（原因见 New 的注释）。
//
// 列与 cloudflare/migrations/0001_init.sql（D1）、store/sqlite 的 schema 逐列对齐：
// 同表名、同列名、同列序、同索引名。时间列均为 Unix **秒**。
//
// 索引用 gorm index 标签声明，由 AutoMigrate 幂等创建：
//   - idx_otps_expiry (expires_at, status)：Cron/清理协程按过期时间清理未读项；
//   - idx_otps_read (read_at)：清理已读超时项。
//
// 两个时间列显式关掉 GORM 的自动时间填充（autoCreateTime:false/autoUpdateTime:false）：
// 它们不是 Go 的 time.Time，且值由本包统一按 Unix 秒计算，交给 GORM 自动填会写错单位。
type otpRecord struct {
	TokenHash string `gorm:"column:token_hash;type:varchar(64);primaryKey;comment:HMAC-SHA256(收件人号码) 的十六进制"`
	Code      string `gorm:"column:code;type:varchar(32);not null;comment:提取出的验证码"`

	// Status 与 ExpiresAt 组成清理索引 idx_otps_expiry：
	// Cleanup 的「未读且已过期」条件是 (status='pending' AND expires_at < now)，
	// 与 store/sqlite 的清理条件一致。
	Status    string `gorm:"column:status;type:varchar(16);not null;default:'pending';index:idx_otps_expiry,priority:2;comment:pending 待读取 / read 已读取（软删除）"`
	ExpiresAt int64  `gorm:"column:expires_at;type:bigint;not null;index:idx_otps_expiry,priority:1;comment:过期时间（Unix 秒）"`

	ReadAt *int64 `gorm:"column:read_at;type:bigint;index:idx_otps_read;comment:读取时间（Unix 秒），未读取为 NULL"`

	CreatedAt int64 `gorm:"column:created_at;type:bigint;not null;autoCreateTime:false;comment:写入时间（Unix 秒）"`
	UpdatedAt int64 `gorm:"column:updated_at;type:bigint;not null;autoUpdateTime:false;comment:最近一次更新时间（Unix 秒）"`
}

// TableName 指定表名，与 D1 / SQLite 版一致
func (otpRecord) TableName() string {
	return "otps"
}

// 各类 SQL。全部用 `?` 占位符交给驱动转义，token/code 只作为参数传入，不拼进 SQL。
//
// 覆盖写用 MySQL 的 UPSERT（ON DUPLICATE KEY UPDATE）而不是「先 SELECT 判断再 INSERT/UPDATE」：
// 后者两步之间存在窗口，并发下会漏写或双写。VALUES(col) 是 MySQL 5.7/8.x 通用的
// 引用新值写法（8.0.20 起官方推荐行别名语法，但那需要 8.0.19+，这里取兼容性更广的一种）。
const (
	sqlUpsertCode = `
INSERT INTO otps (token_hash, code, status, expires_at, read_at, created_at, updated_at)
VALUES (?, ?, '` + statusPending + `', ?, NULL, ?, ?)
ON DUPLICATE KEY UPDATE
  code       = VALUES(code),
  status     = '` + statusPending + `',
  expires_at = VALUES(expires_at),
  read_at    = NULL,
  updated_at = VALUES(updated_at)`

	// GORM 的 Raw().Scan(&string) 在零行时**不会**返回 ErrRecordNotFound，
	// 而是保持目标变量为零值 "" —— 这与 database/sql 的 QueryRow().Scan() 返回
	// sql.ErrNoRows 的行为不同。若只按 error 判断，未命中的 token 会被当成
	// 「命中且验证码为空串」返回 ok=true，直接破坏阅后即焚语义。
	// 因此改用 SQL 层的 COUNT 判定：status/expires_at 条件与取值语句完全一致，
	// 命中才去取 code。多一次查询是必要的代价。
	sqlCountPendingByToken = `
SELECT COUNT(*) FROM otps
WHERE token_hash = ? AND status = '` + statusPending + `' AND expires_at > ?`

	sqlSelectPending = `
SELECT code FROM otps
WHERE token_hash = ? AND status = '` + statusPending + `' AND expires_at > ?`

	sqlDeleteByToken = `DELETE FROM otps WHERE token_hash = ?`

	// 抢占式更新：WHERE 条件与 SQLite 版 RETURNING 语句的 WHERE 完全一致
	// （pending 且未过期），命中才翻成 read 并写 read_at/updated_at。
	sqlClaimRead = `
UPDATE otps SET status = '` + statusRead + `', read_at = ?, updated_at = ?
WHERE token_hash = ? AND status = '` + statusPending + `' AND expires_at > ?`

	sqlSelectCodeByToken = `SELECT code FROM otps WHERE token_hash = ?`

	sqlDeleteReadExpired = `
DELETE FROM otps WHERE status = '` + statusRead + `' AND read_at IS NOT NULL AND read_at < ?`

	sqlDeleteUnreadExpired = `
DELETE FROM otps WHERE status = '` + statusPending + `' AND expires_at < ?`

	sqlCountPending = `
SELECT COUNT(*) FROM otps WHERE status = '` + statusPending + `' AND expires_at > ?`
)

// Store 基于 MySQL 的验证码存储。
//
// 持 *gorm.DB 而不是自己建连接：MySQL 模式下 main.go 已经建好了 GORM 连接
// （短信记录归档用同一条连接），复用它可以复用连接池配置与生命周期管理——
// 本实现不拥有该连接，因此也不提供 Close（由 main 统一管理）。
type Store struct {
	db  *gorm.DB
	ttl time.Duration
}

// New 在 db 上建 otps 表并返回一个 Store。ttl 决定 Set 写入的 expires_at。
//
// 读写全部用原生 SQL 而不是 GORM 的 ORM 接口，原因是 GetAndDelete 必须是一次
// 「条件 UPDATE 抢占」：GORM 的 Updates/Save 拿不到「是否命中」以外的精确语义，
// 而 RowsAffected 才是这里判定抢占成功的唯一依据，直接 Exec 最贴近 SQL 语义。
// 建表仍然用 AutoMigrate（见 otpRecord 注释），二者不冲突。
//
// 构造函数可以返回 error（AutoMigrate 需要 DDL 权限，失败必须让启动中止），
// 但 store.Store 的方法签名被上层锁死不带 error，失败只能在内部记日志。
func New(db *gorm.DB, ttl time.Duration) (*Store, error) {
	if db == nil {
		return nil, errors.New("MySQL 验证码存储需要一个非 nil 的 *gorm.DB")
	}
	if err := db.AutoMigrate(&otpRecord{}); err != nil {
		return nil, err
	}
	return &Store{db: db, ttl: ttl}, nil
}

// Set 写入或覆盖验证码：同一 token 重复写入时新码覆盖旧码，旧码立即失效。
// 覆盖一条已读记录时把它翻回 pending 并清空 read_at，与内存实现「覆盖即新码」一致。
// 失败只记录日志：验证码写不进去会影响后续取码，但不应让整条短信处理链路失败。
func (s *Store) Set(token, code string) {
	now := nowSeconds()
	expires := now + int64(s.ttl.Seconds())

	if err := s.db.Exec(sqlUpsertCode, token, code, expires, now, now).Error; err != nil {
		// 只记录 token 前 8 位，绝不记录验证码明文
		log.Printf("[验证码存储] 写入失败 token_prefix=%s err=%v", logToken(token), err)
	}
}

// Get 读取验证码但不使其失效；token 不存在、已过期或已被读取过时返回 ok=false。
// 查询出错同样按未命中返回：调用方无法区分「不存在」与「出错」，
// 统一回落 ok=false 与内存实现的语义一致。
func (s *Store) Get(token string) (string, bool) {
	code, ok := s.pendingCode(token)
	if !ok {
		return "", false
	}
	return code, true
}

// pendingCode 读取一条 pending 且未过期的验证码。
// 先 COUNT 再 SELECT：GORM 的 Raw().Scan(&code) 在零行时不报错而是留下空串，
// 无法据此区分「不存在」与「存在但为空」，故用 COUNT 判定命中（见 sqlCountPendingByToken）。
func (s *Store) pendingCode(token string) (string, bool) {
	now := nowSeconds()

	var n int
	if err := s.db.Raw(sqlCountPendingByToken, token, now).Scan(&n).Error; err != nil {
		log.Printf("[验证码存储] 读取失败 token_prefix=%s err=%v", logToken(token), err)
		return "", false
	}
	if n == 0 {
		return "", false
	}

	var code string
	if err := s.db.Raw(sqlSelectPending, token, now).Scan(&code).Error; err != nil {
		log.Printf("[验证码存储] 读取失败 token_prefix=%s err=%v", logToken(token), err)
		return "", false
	}
	return code, true
}

// Delete 删除指定验证码；token 不存在时无副作用。
// 这里是物理删除，与 GetAndDelete 的软删除不同：调用方明确要求移除该条目。
func (s *Store) Delete(token string) {
	if err := s.db.Exec(sqlDeleteByToken, token).Error; err != nil {
		log.Printf("[验证码存储] 删除失败 token_prefix=%s err=%v", logToken(token), err)
	}
}

// GetAndDelete 原子地读取验证码并使其失效（阅后即焚）。
//
// MySQL 没有 UPDATE ... RETURNING，因此拆成「条件 UPDATE 抢占」+「SELECT 取回」：
//
//  1. UPDATE ... WHERE token_hash=? AND status='pending' AND expires_at>now
//     单行 UPDATE 在 InnoDB 下会加行锁，并发调用方里只有一个能把 status 从 pending
//     翻成 read，其余全部匹配不到行；
//  2. RowsAffected()==1 才说明本次调用抢占成功，==0 即未命中（不存在/已读/已过期）；
//  3. 确认抢占成功后再 SELECT 取回 code。
//
// 禁止拆成「先 SELECT 判断再 UPDATE」——两步之间存在窗口，会让同一条码被取走两次。
// 这里唯一可接受的竞态是：抢占成功后、SELECT 之前恰好有一次新的 Set 覆盖写入，
// 此时返回的是新码；这与「覆盖即新码，旧码立即失效」的既定语义一致。
//
// 失败（含 SELECT 失败）一律按未命中 ok=false 返回并记日志：
// Store 方法不返回 error，取码失败与「没有码」对调用方是同一件事。
func (s *Store) GetAndDelete(token string) (string, bool) {
	now := nowSeconds()

	res := s.db.Exec(sqlClaimRead, now, now, token, now)
	if err := res.Error; err != nil {
		log.Printf("[验证码存储] 取码失败 token_prefix=%s err=%v", logToken(token), err)
		return "", false
	}
	// token_hash 是主键，命中的行数只能是 0 或 1；!= 1 一律按未命中处理
	if res.RowsAffected != 1 {
		return "", false
	}

	var code string
	if err := s.db.Raw(sqlSelectCodeByToken, token).Scan(&code).Error; err != nil {
		log.Printf("[验证码存储] 读取已抢占的验证码失败 token_prefix=%s err=%v", logToken(token), err)
		return "", false
	}
	return code, true
}

// Cleanup 物理清理两类记录，返回清理条数：
//  1. 已读且读取时间超过 readRetentionSeconds 的记录（软删除的收尾）；
//  2. 未读但已过期的记录。
//
// 与 cloudflare/main.go 的 cleanupExpired、store/sqlite 的 Cleanup 行为一致。
// 任一条 DELETE 失败时返回已累计的条数，全部失败即 0：
// 清理协程只按返回值打日志，签名锁死没有错误上抛通道。
func (s *Store) Cleanup() int {
	now := nowSeconds()
	removed := 0

	res := s.db.Exec(sqlDeleteReadExpired, now-readRetentionSeconds)
	if err := res.Error; err != nil {
		log.Printf("[验证码存储] 清理已读记录失败 err=%v", err)
		return 0
	}
	removed += int(res.RowsAffected)

	res = s.db.Exec(sqlDeleteUnreadExpired, now)
	if err := res.Error; err != nil {
		log.Printf("[验证码存储] 清理过期记录失败 err=%v", err)
		return removed
	}
	removed += int(res.RowsAffected)
	return removed
}

// Len 返回当前未失效的条目数：pending 且未过期，正好是 Cleanup 之后 Get 仍可读的集合。
// 已读记录不计入（Get 读不到），与内存实现「取走即减一」的语义对齐。
func (s *Store) Len() int {
	var n int
	if err := s.db.Raw(sqlCountPending, nowSeconds()).Scan(&n).Error; err != nil {
		log.Printf("[验证码存储] 统计失败 err=%v", err)
		return 0
	}
	return n
}

// nowSeconds 当前时间的 Unix 秒，与 otpRecord 各时间列的单位一致（同 D1 版）。
func nowSeconds() int64 {
	return time.Now().Unix()
}

// logToken 把 token 截断到日志允许的前缀长度。
// token 是手机号 HMAC-SHA256 的十六进制串，完整值等同缓存 key，不得入日志。
func logToken(token string) string {
	if len(token) > tokenLogPrefixLen {
		return token[:tokenLogPrefixLen]
	}
	return token
}

// 注意：本实现刻意不提供 Close()。它复用 main.go 建立的 *gorm.DB，不拥有连接，
// 关掉它会影响同连接上的短信记录归档；连接生命周期由 main 统一收口。
// context 同理：Store 接口不带 ctx（见 store.go 注释），实现内部统一用默认上下文。
