package d1sqltest

import (
	"context"
	"testing"

	"smsserver/cloudflare/otp"
)

// 窗口常量：30 天（见 otp.ReadArchiveWindowSeconds）。
const window = int64(30 * 24 * 3600)

// TestCleanupReadArchive 实证每月一次批量清理（Cron，见 wrangler.jsonc）的语义：
//   - 已消费（read）且超出 30 天窗口 → 物理删除
//   - 已消费但窗口内 → 保留
//   - 未读（pending，即使 expires_at 早已"过期"）→ 严禁自动删除，保留
//   - 提取失败（failed）→ 严禁自动删除，保留
//
// 时间轴（now = 3,000,000，阈值 = now - 30 天 = 408,000）：
//
//	h1 read_at=100,000  → 超出窗口 → 删除
//	h2 read_at=500,000  → 窗口内 → 保留
//	h3 pending，expires_at=1500（早已"过期"）→ 保留（未读严禁删除）
//	h4 failed           → 保留（失败标记严禁删除）
func TestCleanupReadArchive(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()

	// h1：消费于 100,000，远超 30 天窗口
	if err := otp.SaveOTP(ctx, db, "h1", "111111", "验证码111111", 1000, 9_999_999_999); err != nil {
		t.Fatalf("SaveOTP(h1): %v", err)
	}
	if _, err := otp.TakeCode(ctx, db, "h1", 100_000); err != nil {
		t.Fatalf("TakeCode(h1): %v", err)
	}

	// h2：消费于 500,000，窗口内（500,000 > 408,000）
	if err := otp.SaveOTP(ctx, db, "h2", "222222", "验证码222222", 1000, 9_999_999_999); err != nil {
		t.Fatalf("SaveOTP(h2): %v", err)
	}
	if _, err := otp.TakeCode(ctx, db, "h2", 500_000); err != nil {
		t.Fatalf("TakeCode(h2): %v", err)
	}

	// h3：未读且"过期"已久（expires_at=1500 « now=3,000,000）
	if err := otp.SaveOTP(ctx, db, "h3", "333333", "验证码333333", 1000, 1500); err != nil {
		t.Fatalf("SaveOTP(h3): %v", err)
	}

	// h4：提取失败标记
	if err := otp.SaveFailure(ctx, db, "h4", "无验证码的短信", 1000); err != nil {
		t.Fatalf("SaveFailure(h4): %v", err)
	}

	if n := count(t, db); n != 4 {
		t.Fatalf("清理前行数 = %d，期望 4", n)
	}

	now := int64(3_000_000)
	nDeleted, err := otp.CleanupReadArchive(ctx, db, now)
	if err != nil {
		t.Fatalf("CleanupReadArchive: %v", err)
	}
	if nDeleted != 1 {
		t.Errorf("删除数 = %d，期望 1（只有 h1）", nDeleted)
	}

	rows, err := db.Query(`SELECT token_hash, status FROM otps`)
	if err != nil {
		t.Fatalf("查询残留行失败：%v", err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var h, status string
		if err := rows.Scan(&h, &status); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[h] = status
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("清理后残留行 = %v，期望 3 行（h2/h3/h4）", got)
	}
	if got["h3"] != "pending" {
		t.Errorf("未读记录 h3 被删除或状态改变（status=%q），违反「未读严禁自动删除」", got["h3"])
	}
	if got["h4"] != "failed" {
		t.Errorf("失败标记 h4 被删除或状态改变（status=%q），违反「失败标记严禁删除」", got["h4"])
	}
}

// TestCleanupReadArchiveBoundary 窗口边界：read_at == now-窗口 不删（条件是 < 严格小于），
// read_at == now-窗口-1 删除。
func TestCleanupReadArchiveBoundary(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()

	now := int64(3_000_000)
	threshold := now - window // 408,000
	// keep: read_at = threshold（不删）；drop: read_at = threshold-1（删）
	for i, h := range []string{"keep", "drop"} {
		if err := otp.SaveOTP(ctx, db, h, "123456", "验证码123456", 1000, 9_999_999_999); err != nil {
			t.Fatalf("SaveOTP(%s): %v", h, err)
		}
		if _, err := otp.TakeCode(ctx, db, h, threshold-int64(i)); err != nil {
			t.Fatalf("TakeCode(%s): %v", h, err)
		}
	}

	nDeleted, err := otp.CleanupReadArchive(ctx, db, now)
	if err != nil {
		t.Fatalf("CleanupReadArchive: %v", err)
	}
	if nDeleted != 1 {
		t.Errorf("边界清理删除数 = %d，期望 1（只删 read_at < 阈值的 drop）", nDeleted)
	}
	if n := count(t, db); n != 1 {
		t.Errorf("边界清理后行数 = %d，期望 1（保留 keep）", n)
	}
	var h string
	if err := db.QueryRow(`SELECT token_hash FROM otps`).Scan(&h); err != nil {
		t.Fatalf("查询残留行失败：%v", err)
	}
	if h != "keep" {
		t.Errorf("残留行 = %q，期望 keep", h)
	}
}

// TestCleanupReadArchiveKeepsPendingAndFailedAcrossMonths 跨月实证：
// 连续多次（模拟多个月）执行清理，pending 与 failed 记录始终保留，
// 只有超出窗口的 read 记录被逐步清理。
func TestCleanupReadArchiveKeepsPendingAndFailedAcrossMonths(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()

	if err := otp.SaveOTP(ctx, db, "pending-forever", "111111", "验证码111111", 1000, 9_999_999_999); err != nil {
		t.Fatalf("SaveOTP: %v", err)
	}
	if err := otp.SaveFailure(ctx, db, "failed-forever", "始终没提到码", 1000); err != nil {
		t.Fatalf("SaveFailure: %v", err)
	}
	// 一条消费于 t=1000 的记录，一个月后（t=1000+31天）应被清掉
	if err := otp.SaveOTP(ctx, db, "old-read", "222222", "验证码222222", 1000, 9_999_999_999); err != nil {
		t.Fatalf("SaveOTP(old-read): %v", err)
	}
	if _, err := otp.TakeCode(ctx, db, "old-read", 1000); err != nil {
		t.Fatalf("TakeCode: %v", err)
	}

	// 第一个月内清理：old-read 仍在窗口内，保留
	if n, err := otp.CleanupReadArchive(ctx, db, 1000+window-1); err != nil || n != 0 {
		t.Fatalf("窗口内清理 = (%d, %v)，期望 (0, nil)", n, err)
	}
	if n := count(t, db); n != 3 {
		t.Fatalf("窗口内清理后行数 = %d，期望 3", n)
	}

	// 第三十一天清理：old-read 出窗被删，pending 与 failed 仍保留
	nDeleted, err := otp.CleanupReadArchive(ctx, db, 1000+window+1)
	if err != nil {
		t.Fatalf("超窗清理: %v", err)
	}
	if nDeleted != 1 {
		t.Errorf("超窗清理删除数 = %d，期望 1", nDeleted)
	}
	if n := count(t, db); n != 2 {
		t.Fatalf("超窗清理后行数 = %d，期望 2（pending-forever + failed-forever）", n)
	}
	var statuses string
	if err := db.QueryRow(`SELECT COUNT(*) FROM otps WHERE status IN ('pending','failed')`).Scan(&statuses); err != nil {
		t.Fatalf("统计: %v", err)
	}
	if statuses != "2" {
		t.Errorf("pending+failed 行数 = %s，期望 2（严禁自动删除）", statuses)
	}
}

// TestCleanupReadArchiveIdempotentAndNoRows 空表清理不报错，重复清理幂等。
func TestCleanupReadArchiveIdempotentAndNoRows(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()

	n, err := otp.CleanupReadArchive(ctx, db, 1000)
	if err != nil {
		t.Fatalf("空表 CleanupReadArchive: %v", err)
	}
	if n != 0 {
		t.Errorf("空表清理返回 %d，期望 0", n)
	}

	if err := otp.SaveOTP(ctx, db, "h1", "123456", "验证码123456", 1000, 9_999_999_999); err != nil {
		t.Fatalf("SaveOTP: %v", err)
	}
	if _, err := otp.TakeCode(ctx, db, "h1", 2000); err != nil {
		t.Fatalf("TakeCode: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := otp.CleanupReadArchive(ctx, db, 2000+window+10); err != nil {
			t.Fatalf("第 %d 次 CleanupReadArchive: %v", i+1, err)
		}
	}
	if n := count(t, db); n != 0 {
		t.Errorf("重复清理后行数 = %d，期望 0", n)
	}
}
