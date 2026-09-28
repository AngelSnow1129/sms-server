//go:build integration

package mysql

import (
	"os"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestGetAndDeleteConcurrentExactlyOneWinner 实证 MySQL 版取码的并发安全性：
// 50 个协程同时抢同一条验证码，必须恰好一个成功，且拿到的是正确的码。
//
// 这是本实现最关键的一条保证。MySQL 没有 UPDATE ... RETURNING，改用的是
// 「条件 UPDATE 抢占 + RowsAffected 判定 + SELECT 取回」，
// 因此必须用真实数据库（真实行锁）验证抢占成立，不能只靠单元测试推断。
//
// 用法（DSN 指向专用测试库，用例会清空 otps）：
//
//	TEST_MYSQL_DSN='root:root@tcp(127.0.0.1:3306)/smsdb_test?...' \
//	  go test -tags=integration -race ./store/mysql/...
func TestGetAndDeleteConcurrentExactlyOneWinner(t *testing.T) {
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("未设置 TEST_MYSQL_DSN，跳过 MySQL 并发实证")
	}

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("连接测试数据库失败: %v", err)
	}
	s, err := New(db, 10*time.Minute)
	if err != nil {
		t.Fatalf("创建 MySQL 存储失败: %v", err)
	}
	if err := db.Exec(`DELETE FROM otps`).Error; err != nil {
		t.Fatalf("清理测试表失败: %v", err)
	}

	const (
		token = "conc-token"
		want  = "123456"
	)
	s.Set(token, want)

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners int
		got     string
	)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if code, ok := s.GetAndDelete(token); ok {
				mu.Lock()
				winners++
				got = code
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if winners != 1 {
		t.Fatalf("并发取码命中数=%d，期望恰好 1（同一验证码只能被取走一次）", winners)
	}
	if got != want {
		t.Fatalf("取到的验证码=%q，期望 %q", got, want)
	}

	// 抢占后该 token 必须彻底不可再读（第二次查询必为未命中）
	if _, ok := s.GetAndDelete(token); ok {
		t.Fatal("已被取走的验证码仍可读，阅后即焚失效")
	}
}
