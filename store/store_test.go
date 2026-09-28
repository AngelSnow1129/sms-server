package store_test

import (
	"path/filepath"
	"testing"
	"time"

	"smsserver/cache"
	"smsserver/store"
	"smsserver/store/sqlite"
)

// storeImpl 登记一个待验证的 store.Store 实现及其构造方式。
// 将来接入 MySQL 实现时，只需在此登记，即可复用下面的全量契约用例。
//
// new 不返回 error：Store 接口本身不含错误通道（见 store.go 注释），
// 构造失败属于接线期问题，在登记处直接 t.Fatalf 即可，不必污染每个用例。
//
// 传入 *testing.T 是为了让实现能拿到 t.TempDir()（临时库路径）与 t.Cleanup()
// （关闭连接），内存实现忽略该参数即可。
type storeImpl struct {
	name string
	new  func(t *testing.T, ttl time.Duration) store.Store
}

// newSQLiteStore 在 t.TempDir() 下建一个临时库，供单个子用例独占使用。
// 每个用例一个文件，避免用例间互相看到对方的 pending 记录（契约里有 Len 断言）。
//
// 不用 :memory: —— database/sql 连接池下每个连接都是独立库，写入会落到与读取
// 不同的库上；Store 已把连接池压到 1，但临时文件能额外保证子用例间彻底隔离。
func newSQLiteStore(t *testing.T, ttl time.Duration) store.Store {
	t.Helper()

	s, err := sqlite.New(filepath.Join(t.TempDir(), "otps.db"), ttl)
	if err != nil {
		t.Fatalf("创建 SQLite 存储失败: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("关闭 SQLite 存储失败: %v", err)
		}
	})
	return s
}

// implementations 当前已接入的存储实现
var implementations = []storeImpl{
	{"内存缓存", func(_ *testing.T, ttl time.Duration) store.Store { return cache.NewOTPCache(ttl) }},
	{"SQLite", newSQLiteStore},
}

// TestStoreContract 对全部实现跑同一组契约用例，保证切换存储不改变业务语义：
// 写入/覆盖、阅后即焚、TTL 失效、清理计数与长度统计。
func TestStoreContract(t *testing.T) {
	for _, impl := range implementations {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			tests := []struct {
				name string
				ttl  time.Duration
				run  func(t *testing.T, s store.Store)
			}{
				{
					name: "写入后可读取",
					ttl:  time.Minute,
					run: func(t *testing.T, s store.Store) {
						s.Set("token", "123456")
						code, ok := s.Get("token")
						if !ok || code != "123456" {
							t.Fatalf("Get = (%q, %v)，期望 (\"123456\", true)", code, ok)
						}
					},
				},
				{
					name: "未写入的 token 读取为 false",
					ttl:  time.Minute,
					run: func(t *testing.T, s store.Store) {
						if code, ok := s.Get("absent"); ok {
							t.Fatalf("不存在的 token 应返回 false，实际 (%q, %v)", code, ok)
						}
					},
				},
				{
					name: "覆盖写入后旧码失效",
					ttl:  time.Minute,
					run: func(t *testing.T, s store.Store) {
						s.Set("token", "111111")
						s.Set("token", "222222")

						code, ok := s.Get("token")
						if !ok || code != "222222" {
							t.Fatalf("覆盖后 Get = (%q, %v)，期望 (\"222222\", true)", code, ok)
						}
					},
				},
				{
					name: "阅后即焚只生效一次",
					ttl:  time.Minute,
					run: func(t *testing.T, s store.Store) {
						s.Set("token", "123456")

						code, ok := s.GetAndDelete("token")
						if !ok || code != "123456" {
							t.Fatalf("首次 GetAndDelete = (%q, %v)，期望 (\"123456\", true)", code, ok)
						}
						if code, ok := s.GetAndDelete("token"); ok {
							t.Fatalf("第二次 GetAndDelete 仍返回 %q，阅后即焚失效", code)
						}
						if code, ok := s.Get("token"); ok {
							t.Fatalf("GetAndDelete 之后 Get 仍返回 %q", code)
						}
					},
				},
				{
					name: "过期后不可读",
					ttl:  30 * time.Millisecond,
					run: func(t *testing.T, s store.Store) {
						s.Set("token", "123456")
						time.Sleep(60 * time.Millisecond)

						if code, ok := s.Get("token"); ok {
							t.Fatalf("过期后 Get 仍返回 %q", code)
						}
						if code, ok := s.GetAndDelete("token"); ok {
							t.Fatalf("过期后 GetAndDelete 仍返回 %q", code)
						}
					},
				},
				{
					name: "Delete 后不可读",
					ttl:  time.Minute,
					run: func(t *testing.T, s store.Store) {
						s.Set("token", "123456")
						s.Delete("token")

						if _, ok := s.Get("token"); ok {
							t.Fatal("Delete 之后仍可读到")
						}
						// 删除不存在的 token 不应 panic，也不应产生新条目
						s.Delete("absent")
					},
				},
				{
					name: "Cleanup 只清理过期项并保留有效项",
					ttl:  40 * time.Millisecond,
					run: func(t *testing.T, s store.Store) {
						s.Set("expired-1", "111111")
						s.Set("expired-2", "222222")
						time.Sleep(80 * time.Millisecond)
						s.Set("alive", "333333")

						if removed := s.Cleanup(); removed != 2 {
							t.Fatalf("Cleanup 移除 %d 个，期望 2 个", removed)
						}
						if n := s.Len(); n != 1 {
							t.Fatalf("Cleanup 后 Len = %d，期望 1", n)
						}
						if code, ok := s.Get("alive"); !ok || code != "333333" {
							t.Fatalf("未过期条目被误删：(%q, %v)", code, ok)
						}
					},
				},
				{
					name: "空存储 Cleanup 返回 0",
					ttl:  time.Minute,
					run: func(t *testing.T, s store.Store) {
						if removed := s.Cleanup(); removed != 0 {
							t.Fatalf("空存储 Cleanup = %d，期望 0", removed)
						}
					},
				},
				{
					name: "Len 统计未失效条目",
					ttl:  time.Minute,
					run: func(t *testing.T, s store.Store) {
						if n := s.Len(); n != 0 {
							t.Fatalf("初始 Len = %d，期望 0", n)
						}

						s.Set("a", "111111")
						s.Set("b", "222222")
						s.Set("a", "333333") // 覆盖不应增加计数
						if n := s.Len(); n != 2 {
							t.Fatalf("Len = %d，期望 2（覆盖写入不应增加计数）", n)
						}

						s.GetAndDelete("a")
						if n := s.Len(); n != 1 {
							t.Fatalf("取走一条后 Len = %d，期望 1", n)
						}
					},
				},
			}

			for _, tt := range tests {
				tt := tt
				t.Run(tt.name, func(t *testing.T) {
					tt.run(t, impl.new(t, tt.ttl))
				})
			}
		})
	}
}
