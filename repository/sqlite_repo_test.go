package repository

// SQLite 仓储的行为测试：验证影子模型 sqliteSMSRecord 建表、以及 DML 走
// model.SMSRecord 读写同一张表。纯 SQLite 无外部依赖，不进 integration 标签；
// MySQL 版对应行为由 integration_test.go 覆盖。

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"smsserver/model"
)

func openSQLiteRepo(t *testing.T) *SQLiteRepository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "repo_test.db")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开 SQLite 失败: %v", err)
	}
	repo, err := NewSQLiteRepository(db)
	if err != nil {
		t.Fatalf("建 SQLite 仓储失败: %v", err)
	}
	return repo
}

func TestSQLiteRepositoryCreateAndQuery(t *testing.T) {
	repo := openSQLiteRepo(t)

	rec := &model.SMSRecord{
		Provider:   "smsforward",
		Sender:     "+8613800000000",
		Recipient:  "+8613900000000",
		Body:       "【云堡垒机】您的验证码为Gcfx",
		ChannelID:  "android-main",
		ReceivedAt: time.Now(),
	}
	if err := repo.Create(rec); err != nil {
		t.Fatalf("Create 失败: %v", err)
	}
	if rec.ID == 0 {
		t.Fatal("Create 后主键未回填")
	}

	got, err := repo.FindByRecipient("+8613900000000")
	if err != nil {
		t.Fatalf("FindByRecipient 失败: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("期望 1 条记录，实际 %d", len(got))
	}
	if got[0].Provider != "smsforward" || got[0].Body != rec.Body {
		t.Fatalf("字段往返不一致: %+v", got[0])
	}
	if got[0].CreatedAt.IsZero() {
		t.Fatal("created_at 未被填充")
	}
}

func TestSQLiteRepositoryNewRequiresDB(t *testing.T) {
	if _, err := NewSQLiteRepository(nil); err == nil {
		t.Fatal("nil *gorm.DB 应返回错误")
	}
}
