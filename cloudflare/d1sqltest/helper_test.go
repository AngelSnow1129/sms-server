// d1sqltest 是独立于 cloudflare 主模块的测试模块（有自己的 go.mod）。
//
// 为什么独立成模块：D1 的 SQL 语义（UPDATE ... RETURNING 原子性、upsert、Cron 清理）
// 只能在真实的 SQLite 方言上实证，而引入 SQLite 驱动会往 cloudflare/go.mod 里加入
// 几十个间接依赖，污染 Deploy to Cloudflare 的构建（wrangler 会执行 npm run build →
// GOOS=js GOARCH=wasm go build）。放在带独立 go.mod 的子目录里，主模块既看不到这些
// 依赖，也不会被 `go test ./...` 递归到（嵌套模块会被自动排除），
// 构建产物 build/app.wasm 因此完全不受影响。
//
// 被测对象是生产代码本身：SQL 语句常量与封装全部来自 smsserver/cloudflare/otp 包
// （经 replace 指向父目录），表结构直接读 migrations/0001_init.sql，
// 因此这里实证的就是线上跑的那几条语句，而不是复制的副本。
//
// 运行方式（主模块的 `go test ./...` 不会递归进嵌套模块，必须显式指定）：
//
//	cd cloudflare/d1sqltest && go test -race ./...
package d1sqltest

import (
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	// SQLite 驱动，用来在真实 SQLite 方言上实证 D1 的 SQL 语义。
	// 纯 Go 实现（modernc.org/sqlite，无 cgo），随测试模块构建，不触碰主模块依赖。
	_ "modernc.org/sqlite"
)

// schema 建表语句：按字典序读取 migrations/ 目录下全部 migration
// （0001_init.sql → 0002_add_raw_content.sql → …），避免测试里的表结构与线上漂移。
func schema(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("..", "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取 migrations 目录失败：%v", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatalf("migrations 目录下未找到任何 .sql")
	}
	var b strings.Builder
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("读取 migration %s 失败：%v", name, err)
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	return b.String()
}

// statements 把 migration 文本切成可执行语句：先去掉 -- 行注释（migration 的注释里
// 含有 ; 与「；」，直接按 ; 切会切断 CREATE TABLE），再按 ; 切分。
// 与 wrangler d1 migrations apply 的切分方式一致（逐语句执行）。
func statements(s string) []string {
	var kept []string
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		kept = append(kept, line)
	}
	var out []string
	for _, stmt := range strings.Split(strings.Join(kept, "\n"), ";") {
		if strings.TrimSpace(stmt) != "" {
			out = append(out, stmt)
		}
	}
	return out
}

// newDB 在一个临时文件上建库，并执行 0001_init.sql 的全部语句。
func newDB(t *testing.T) *sql.DB {
	t.Helper()
	// 单连接：让并发测试真正争抢同一把写锁，从而检验单条语句的原子性；
	// 多连接时 SQLite 会直接返回 SQLITE_BUSY，反而掩盖语义问题。
	return openDB(t, 1)
}

// newConcurrentDB 开多个连接并启用 WAL + busy_timeout，贴近生产 D1 的并发形态：
// 多个请求（Workers 实例）各有自己的连接同时打向同一张表。
// busy_timeout 让并发写请求排队而不是立刻报 SQLITE_BUSY，
// 这样测出来的「恰好一个赢家」才是语句原子性的证据，而不是锁竞争的假象。
func newConcurrentDB(t *testing.T, maxConns int) *sql.DB {
	t.Helper()
	return openDB(t, maxConns)
}

func openDB(t *testing.T, maxConns int) *sql.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "d1.db") + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns)
	t.Cleanup(func() { db.Close() })

	for _, stmt := range statements(schema(t)) {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("执行 migration 语句失败 %q：%v", stmt, err)
		}
	}
	return db
}

// row 一条 otps 记录，用于断言状态。
type row struct {
	code       string
	status     string
	expiresAt  int64
	readAt     sql.NullInt64
	rawContent string
	createdAt  int64
	updatedAt  int64
}

func fetch(t *testing.T, db *sql.DB, tokenHash string) row {
	t.Helper()
	var r row
	err := db.QueryRow(
		`SELECT code, status, expires_at, read_at, raw_content, created_at, updated_at FROM otps WHERE token_hash = ?`,
		tokenHash).Scan(&r.code, &r.status, &r.expiresAt, &r.readAt, &r.rawContent, &r.createdAt, &r.updatedAt)
	if err != nil {
		t.Fatalf("查询 %q 失败：%v", tokenHash, err)
	}
	return r
}

func count(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM otps`).Scan(&n); err != nil {
		t.Fatalf("count 失败：%v", err)
	}
	return n
}
