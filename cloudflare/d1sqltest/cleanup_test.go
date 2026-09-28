package d1sqltest

import (
	"context"
	"testing"

	"smsserver/cloudflare/otp"
)

// TestCleanupExpired 实证 Cron 清理（每 30 分钟，见 wrangler.jsonc）的三条语义：
//   - 已读超 otp.ReadRetentionSeconds（600s）→ 物理删除
//   - 未读但已过期 → 物理删除
//   - 窗口内已读 / 未读未过期 → 保留
//
// 时间轴（read_at 由取码语句写入）：
//
//	t=1000 写入两条
//	t=1050 取走 h1（read_at=1050，软删除保留）
//	t=1700 清理：now-600=1100 → read_at=1050 < 1100，h1 被删；h2 未读已过期被删
func TestCleanupExpired(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()

	// h1：写入后立刻取走 → read_at = 1050
	if err := otp.SaveOTP(ctx, db, "h1", "111111", 1000, 1300); err != nil {
		t.Fatalf("SaveOTP(h1): %v", err)
	}
	if _, err := otp.TakeCode(ctx, db, "h1", 1050); err != nil {
		t.Fatalf("TakeCode(h1): %v", err)
	}

	// h2：未读且已过期（expires_at=1100 < now=1700）
	if err := otp.SaveOTP(ctx, db, "h2", "222222", 1000, 1100); err != nil {
		t.Fatalf("SaveOTP(h2): %v", err)
	}

	// h3：窗口内已读（read_at=1200，now-600=1100，1200 不小于 1100 → 保留）
	if err := otp.SaveOTP(ctx, db, "h3", "333333", 1000, 9_999_999_999); err != nil {
		t.Fatalf("SaveOTP(h3): %v", err)
	}
	if _, err := otp.TakeCode(ctx, db, "h3", 1200); err != nil {
		t.Fatalf("TakeCode(h3): %v", err)
	}

	// h4：未读且未过期 → 保留
	if err := otp.SaveOTP(ctx, db, "h4", "444444", 1000, 9_999_999_999); err != nil {
		t.Fatalf("SaveOTP(h4): %v", err)
	}

	if n := count(t, db); n != 4 {
		t.Fatalf("清理前行数 = %d，期望 4", n)
	}

	nRead, nExpired, err := otp.CleanupExpired(ctx, db, 1700)
	if err != nil {
		t.Fatalf("CleanupExpired: %v", err)
	}
	if nRead != 1 {
		t.Errorf("已读超时删除数 = %d，期望 1（只有 h1）", nRead)
	}
	if nExpired != 1 {
		t.Errorf("未读过期删除数 = %d，期望 1（只有 h2）", nExpired)
	}

	remaining := map[string]bool{"h3": true, "h4": true}
	rows, err := db.Query(`SELECT token_hash FROM otps`)
	if err != nil {
		t.Fatalf("查询残留行失败：%v", err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[h] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(got) != len(remaining) {
		t.Fatalf("清理后残留行 = %v，期望 %v", got, remaining)
	}
	for h := range remaining {
		if !got[h] {
			t.Errorf("清理后 %s 应保留（窗口内已读 / 未读未过期），实际被删", h)
		}
	}
}

// TestCleanupExpiredBoundary 保留窗口边界：read_at == now-600 不删（条件是 < 严格小于），
// read_at == now-601 删除。
func TestCleanupExpiredBoundary(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()

	for i, h := range []string{"keep", "drop"} {
		if err := otp.SaveOTP(ctx, db, h, "123456", 1000, 9_999_999_999); err != nil {
			t.Fatalf("SaveOTP(%s): %v", h, err)
		}
		// read_at 取 500 / 499，配合 now=1100 → 阈值 500
		if _, err := otp.TakeCode(ctx, db, h, int64(500-i)); err != nil {
			t.Fatalf("TakeCode(%s): %v", h, err)
		}
	}

	nRead, _, err := otp.CleanupExpired(ctx, db, 1100)
	if err != nil {
		t.Fatalf("CleanupExpired: %v", err)
	}
	if nRead != 1 {
		t.Errorf("边界清理删除数 = %d，期望 1（只删 read_at < 500 的 drop）", nRead)
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

// TestCleanupExpiredKeepsReadRowInsideWindow 窗口内已读记录保留，用于对账；
// 跨过窗口后同一次 Cron 会把它清掉。
func TestCleanupExpiredKeepsReadRowInsideWindow(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()

	if err := otp.SaveOTP(ctx, db, "h1", "123456", 1000, 9_999_999_999); err != nil {
		t.Fatalf("SaveOTP: %v", err)
	}
	if _, err := otp.TakeCode(ctx, db, "h1", 1000); err != nil {
		t.Fatalf("TakeCode: %v", err)
	}

	// 窗口内（1000 + 599）不清理
	if _, _, err := otp.CleanupExpired(ctx, db, 1000+otp.ReadRetentionSeconds-1); err != nil {
		t.Fatalf("CleanupExpired: %v", err)
	}
	if n := count(t, db); n != 1 {
		t.Fatalf("窗口内清理后行数 = %d，期望 1", n)
	}
	// 记录仍可被查到（对账用途：code 与 read_at 都还在）
	if r := fetch(t, db, "h1"); r.code != "123456" || !r.readAt.Valid {
		t.Errorf("窗口内已读记录 = (code=%q, read_at=%v)，应保留明文与读取时间", r.code, r.readAt)
	}

	// 跨过窗口后清理
	if _, _, err := otp.CleanupExpired(ctx, db, 1000+otp.ReadRetentionSeconds+1); err != nil {
		t.Fatalf("CleanupExpired: %v", err)
	}
	if n := count(t, db); n != 0 {
		t.Errorf("超出窗口后行数 = %d，期望 0", n)
	}
}

// TestCleanupExpiredIdempotentAndNoRows 空表清理不报错，重复清理幂等。
func TestCleanupExpiredIdempotentAndNoRows(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()

	nRead, nExpired, err := otp.CleanupExpired(ctx, db, 1000)
	if err != nil {
		t.Fatalf("空表 CleanupExpired: %v", err)
	}
	if nRead != 0 || nExpired != 0 {
		t.Errorf("空表清理返回 (%d, %d)，期望 (0, 0)", nRead, nExpired)
	}

	if err := otp.SaveOTP(ctx, db, "h1", "123456", 1000, 1100); err != nil {
		t.Fatalf("SaveOTP: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, _, err := otp.CleanupExpired(ctx, db, 2000); err != nil {
			t.Fatalf("第 %d 次 CleanupExpired: %v", i+1, err)
		}
	}
	if n := count(t, db); n != 0 {
		t.Errorf("重复清理后行数 = %d，期望 0", n)
	}
}
