package d1sqltest

import (
	"context"
	"database/sql"
	"testing"

	"smsserver/cloudflare/otp"
)

// 窗口常量：30 天（见 otp.ReadArchiveWindowSeconds）。
const window = int64(30 * 24 * 3600)

// markRead 写入一条码并在 readAt 时刻消费，得到 status='read' 的取码位。
func markRead(t *testing.T, db *sql.DB, readAt int64) {
	t.Helper()
	ctx := context.Background()
	if err := otp.SaveOTP(ctx, db, "111111", "验证码111111", 1000, 9_999_999_999); err != nil {
		t.Fatalf("SaveOTP: %v", err)
	}
	if _, err := otp.TakeCode(ctx, db, readAt); err != nil {
		t.Fatalf("TakeCode(%d): %v", readAt, err)
	}
}

// TestCleanupReadArchiveScenarios 实证每月一次批量清理（Cron，见 wrangler.jsonc）语义。
// 取码位同时只能有一行，故四种状态分别在各自的新库上验证（now=3,000,000，阈值=408,000）：
//   - 旧已读（read_at=100,000）→ 出窗物理删除
//   - 窗口内已读（read_at=500,000）→ 保留
//   - 未读（expires_at=1500，早已"过期"）→ 严禁自动删除，保留 pending
//   - 提取失败（failed）→ 严禁自动删除，保留 failed
func TestCleanupReadArchiveScenarios(t *testing.T) {
	const now = int64(3_000_000)

	tests := []struct {
		name          string
		setup         func(t *testing.T, db *sql.DB)
		wantDeleted   int64
		wantRemaining int
		wantStatus    string // remaining 行的期望状态；wantRemaining=0 时留空
	}{
		{
			name: "旧已读出窗删除", setup: func(t *testing.T, db *sql.DB) { markRead(t, db, 100_000) },
			wantDeleted: 1, wantRemaining: 0,
		},
		{
			name: "窗口内已读保留", setup: func(t *testing.T, db *sql.DB) { markRead(t, db, 500_000) },
			wantDeleted: 0, wantRemaining: 1, wantStatus: "read",
		},
		{
			name: "未读即使过期保留",
			setup: func(t *testing.T, db *sql.DB) {
				if err := otp.SaveOTP(context.Background(), db, "333333", "验证码333333", 1000, 1500); err != nil {
					t.Fatalf("SaveOTP: %v", err)
				}
			},
			wantDeleted: 0, wantRemaining: 1, wantStatus: "pending",
		},
		{
			name: "失败标记保留",
			setup: func(t *testing.T, db *sql.DB) {
				if err := otp.SaveFailure(context.Background(), db, "无验证码的短信", 1000); err != nil {
					t.Fatalf("SaveFailure: %v", err)
				}
			},
			wantDeleted: 0, wantRemaining: 1, wantStatus: "failed",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			db := newDB(t)
			tt.setup(t, db)

			n, err := otp.CleanupReadArchive(context.Background(), db, now)
			if err != nil {
				t.Fatalf("CleanupReadArchive: %v", err)
			}
			if n != tt.wantDeleted {
				t.Errorf("删除数 = %d，期望 %d", n, tt.wantDeleted)
			}
			if nRows := count(t, db); nRows != tt.wantRemaining {
				t.Errorf("清理后行数 = %d，期望 %d", nRows, tt.wantRemaining)
			}
			if tt.wantRemaining == 1 && fetch(t, db).status != tt.wantStatus {
				t.Errorf("残留行 status = %q，期望 %q", fetch(t, db).status, tt.wantStatus)
			}
		})
	}
}

// TestCleanupReadArchiveBoundary 窗口边界：read_at == now-窗口 不删（条件是严格小于 <），
// read_at == now-窗口-1 删除。两种边界各用一个新库。
func TestCleanupReadArchiveBoundary(t *testing.T) {
	now := int64(3_000_000)
	threshold := now - window // 408,000

	// keep：read_at == threshold → 保留
	dbKeep := newDB(t)
	markRead(t, dbKeep, threshold)
	if n, err := otp.CleanupReadArchive(context.Background(), dbKeep, now); err != nil || n != 0 {
		t.Fatalf("边界保留：删除数 = (%d, %v)，期望 (0, nil)", n, err)
	}
	if count(t, dbKeep) != 1 || fetch(t, dbKeep).status != "read" {
		t.Errorf("read_at==阈值 的行应保留")
	}

	// drop：read_at == threshold-1 → 删除
	dbDrop := newDB(t)
	markRead(t, dbDrop, threshold-1)
	if n, err := otp.CleanupReadArchive(context.Background(), dbDrop, now); err != nil || n != 1 {
		t.Fatalf("边界删除：删除数 = (%d, %v)，期望 (1, nil)", n, err)
	}
	if count(t, dbDrop) != 0 {
		t.Errorf("read_at==阈值-1 的行应删除")
	}
}

// TestCleanupReadArchiveReadRowAcrossMonths 已读记录跨月：窗口内清理不删，出窗那一次删除。
func TestCleanupReadArchiveReadRowAcrossMonths(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	markRead(t, db, 1000)

	if n, err := otp.CleanupReadArchive(ctx, db, 1000+window-1); err != nil || n != 0 {
		t.Fatalf("窗口内清理 = (%d, %v)，期望 (0, nil)", n, err)
	}
	if count(t, db) != 1 {
		t.Errorf("窗口内已读记录应保留")
	}
	if n, err := otp.CleanupReadArchive(ctx, db, 1000+window+1); err != nil || n != 1 {
		t.Fatalf("出窗清理 = (%d, %v)，期望 (1, nil)", n, err)
	}
	if count(t, db) != 0 {
		t.Errorf("出窗已读记录应被物理删除")
	}
}

// TestCleanupReadArchiveKeepsPendingAndFailedAcrossMonths 跨月（多次清理）：
// pending 与 failed 取码位始终保留，只有状态可能因新短信改变、绝不因时间被删。
func TestCleanupReadArchiveKeepsPendingAndFailedAcrossMonths(t *testing.T) {
	ctx := context.Background()

	t.Run("pending 跨月保留", func(t *testing.T) {
		db := newDB(t)
		if err := otp.SaveOTP(ctx, db, "111111", "验证码111111", 1000, 1500); err != nil {
			t.Fatalf("SaveOTP: %v", err)
		}
		for _, now := range []int64{1000, 1000 + window, 1000 + 2*window} {
			if n, err := otp.CleanupReadArchive(ctx, db, now); err != nil || n != 0 {
				t.Fatalf("now=%d 清理 = (%d, %v)，期望 (0, nil)", now, n, err)
			}
		}
		if count(t, db) != 1 || fetch(t, db).status != "pending" {
			t.Errorf("未读取码位跨月应保留为 pending")
		}
	})

	t.Run("failed 跨月保留", func(t *testing.T) {
		db := newDB(t)
		if err := otp.SaveFailure(ctx, db, "始终没提到码", 1000); err != nil {
			t.Fatalf("SaveFailure: %v", err)
		}
		for _, now := range []int64{1000, 1000 + window, 1000 + 2*window} {
			if n, err := otp.CleanupReadArchive(ctx, db, now); err != nil || n != 0 {
				t.Fatalf("now=%d 清理 = (%d, %v)，期望 (0, nil)", now, n, err)
			}
		}
		if count(t, db) != 1 || fetch(t, db).status != "failed" {
			t.Errorf("失败取码位跨月应保留为 failed")
		}
	})
}

// TestCleanupReadArchiveIdempotentAndNoRows 空表清理不报错，重复清理幂等。
func TestCleanupReadArchiveIdempotentAndNoRows(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()

	if n, err := otp.CleanupReadArchive(ctx, db, 1000); err != nil || n != 0 {
		t.Fatalf("空表清理 = (%d, %v)，期望 (0, nil)", n, err)
	}

	markRead(t, db, 2000)
	for i := 0; i < 3; i++ {
		if _, err := otp.CleanupReadArchive(ctx, db, 2000+window+10); err != nil {
			t.Fatalf("第 %d 次 CleanupReadArchive: %v", i+1, err)
		}
	}
	if count(t, db) != 0 {
		t.Errorf("重复清理后行数 = %d，期望 0", count(t, db))
	}
}
