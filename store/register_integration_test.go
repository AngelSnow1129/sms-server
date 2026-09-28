//go:build integration

// MySQL 版契约用例的登记表：需要真实数据库，由 TEST_MYSQL_DSN 注入连接串。
// 未设置该变量时用例全部跳过，因此带 `-tags=integration` 且无数据库的环境也不受影响。
//
// 运行方式（注意 DSN 指向**专用测试库**）：
//
//	TEST_MYSQL_DSN='root:root@tcp(127.0.0.1:3306)/smsdb_test?parseTime=true&loc=Local' \
//	  go test -tags=integration -race ./store/...
package store_test

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"smsserver/store"
	mysqlstore "smsserver/store/mysql"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 整个测试进程共用一条连接：契约用例有 9 条，逐条 gorm.Open 会留下 9 个无人关闭的池。
// 连接串来自环境变量，测试进程内不变，因此缓存一次即可。
var (
	mysqlOnce sync.Once
	mysqlDB   *gorm.DB
	mysqlErr  error
)

// openMySQL 惰性连接测试库（首条用例触发），未设置 DSN 时跳过。
// 用例之间靠 cleanMySQL 清表隔离，不复用 SQLite 那种「每用例一个库文件」的做法：
// MySQL 建库需要额外权限，DELETE 对本用例足够。
func openMySQL(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("未设置 TEST_MYSQL_DSN，跳过 MySQL 契约用例")
	}

	mysqlOnce.Do(func() {
		mysqlDB, mysqlErr = gorm.Open(mysql.Open(dsn), &gorm.Config{
			Logger: logger.Default.LogMode(logger.Silent),
		})
	})
	if mysqlErr != nil {
		t.Fatalf("连接测试数据库失败: %v", mysqlErr)
	}
	return mysqlDB
}

// mysqlSeq 为每次构造分配一个互不相同的 token 前缀。
// 契约用例共享同一个库，若两个子用例都用 "token"、"a"、"b" 这些字面量，
// 一个用例的残留会让另一个用例的 Len 断言失真；前缀让每个用例独占命名空间。
var mysqlSeq struct {
	sync.Mutex
	n int
}

func nextMySQLPrefix() string {
	mysqlSeq.Lock()
	defer mysqlSeq.Unlock()
	mysqlSeq.n++
	return fmt.Sprintf("t%04d", mysqlSeq.n)
}

// newMySQLStore 建表、清表，返回一个独占前缀的 MySQL 存储。
// 前缀只作用于本用例写入的 token（整体换命名空间），不改变契约用例自身的语义。
func newMySQLStore(t *testing.T, ttl time.Duration) store.Store {
	t.Helper()

	db := openMySQL(t)

	// New 内部 AutoMigrate 建 otps 表，必须在 DELETE 之前：
	// 首次运行时表还不存在，先 DELETE 会报 1146。
	s, err := mysqlstore.New(db, ttl)
	if err != nil {
		t.Fatalf("创建 MySQL 存储失败: %v", err)
	}
	// 从干净状态开始：契约对 Len 有精确断言，上一条用例的残留会让断言失真
	if err := db.Exec(`DELETE FROM otps`).Error; err != nil {
		t.Fatalf("清理测试表 otps 失败: %v", err)
	}
	return namespacedStore{s: s, prefix: nextMySQLPrefix()}
}

// namespacedStore 给所有 token 加前缀的包装，只用于 MySQL 契约用例的用例间隔离。
// 它不改变任何语义：同一用例内 token 仍然一一对应，只是整体换了命名空间。
type namespacedStore struct {
	s      store.Store
	prefix string
}

func (n namespacedStore) key(token string) string { return n.prefix + "-" + token }

func (n namespacedStore) Set(token, code string) { n.s.Set(n.key(token), code) }
func (n namespacedStore) Delete(token string)    { n.s.Delete(n.key(token)) }

// Cleanup 与 Len 是全局操作，无法按命名空间过滤。它们仍然安全：
// 每条用例开始时库里只有它自己的记录（上一条用例的记录已被 DELETE 清掉），
// 且子用例之间不并行，因此 COUNT(*)/DELETE 的结果就是本用例的记录。
func (n namespacedStore) Cleanup() int { return n.s.Cleanup() }
func (n namespacedStore) Len() int     { return n.s.Len() }

func (n namespacedStore) Get(token string) (string, bool) { return n.s.Get(n.key(token)) }

func (n namespacedStore) GetAndDelete(token string) (string, bool) {
	return n.s.GetAndDelete(n.key(token))
}

// mysqlImplementations 带 integration 标签时把 MySQL 实现登记进契约用例。
// secondPrecision=true：MySQL 版时间列是 Unix 秒，跳过毫秒级 TTL 用例（见 storeImpl）。
var mysqlImplementations = []storeImpl{
	{name: "MySQL", new: newMySQLStore, secondPrecision: true},
}
