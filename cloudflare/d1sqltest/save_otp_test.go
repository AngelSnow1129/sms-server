package d1sqltest

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"smsserver/cloudflare/otp"
)

// TestSaveOTPUpsertOverridesOldCode 实证 upsert 语义：同 token 新码覆盖旧码，
// 且覆盖后 status 回到 pending、read_at 置 NULL（已被取走的旧码不影响新码可取）。
func TestSaveOTPUpsertOverridesOldCode(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()

	// 首次写入
	if err := otp.SaveOTP(ctx, db, "h1", "111111", 1000, 1300); err != nil {
		t.Fatalf("首次 SaveOTP: %v", err)
	}
	// h2：另一号码，有效期足够长，用于验证 upsert 不影响其他记录
	if err := otp.SaveOTP(ctx, db, "h2", "222222", 1000, 9_999_999_999); err != nil {
		t.Fatalf("写入另一个 token 失败：%v", err)
	}

	// 取走 h1：status=read, read_at=1050
	if code, err := otp.TakeCode(ctx, db, "h1", 1050); err != nil || code != "111111" {
		t.Fatalf("TakeCode(h1) = (%q, %v)，期望 (\"111111\", nil)", code, err)
	}

	// 同号码收到新短信：upsert 覆盖
	if err := otp.SaveOTP(ctx, db, "h1", "333333", 2000, 2300); err != nil {
		t.Fatalf("覆盖 SaveOTP: %v", err)
	}

	if n := count(t, db); n != 2 {
		t.Errorf("覆盖后总行数 = %d，期望 2（upsert 不应产生第二行）", n)
	}

	r := fetch(t, db, "h1")
	if r.code != "333333" {
		t.Errorf("覆盖后 code = %q，期望 333333", r.code)
	}
	if r.status != "pending" {
		t.Errorf("覆盖后 status = %q，期望 pending", r.status)
	}
	if r.readAt.Valid {
		t.Errorf("覆盖后 read_at = %d，期望 NULL", r.readAt.Int64)
	}
	if r.expiresAt != 2300 {
		t.Errorf("覆盖后 expires_at = %d，期望 2300", r.expiresAt)
	}
	if r.updatedAt != 2000 {
		t.Errorf("覆盖后 updated_at = %d，期望 2000", r.updatedAt)
	}
	if r.createdAt != 1000 {
		t.Errorf("覆盖后 created_at = %d，期望保留首次写入时间 1000", r.createdAt)
	}

	// 覆盖后可再次取到新码
	if code, err := otp.TakeCode(ctx, db, "h1", 2100); err != nil || code != "333333" {
		t.Errorf("覆盖后 TakeCode = (%q, %v)，期望 (\"333333\", nil)", code, err)
	}

	// 其他 token 不受影响
	if code, err := otp.TakeCode(ctx, db, "h2", 2100); err != nil || code != "222222" {
		t.Errorf("TakeCode(h2) = (%q, %v)，期望 (\"222222\", nil)", code, err)
	}
}

// TestSaveOTPImmediatelyActiveAfterOverwriteExpiredRow 已过期但未被读的记录被同号新码覆盖后立刻可取。
func TestSaveOTPImmediatelyActiveAfterOverwriteExpiredRow(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()

	if err := otp.SaveOTP(ctx, db, "h1", "111111", 1000, 1100); err != nil {
		t.Fatalf("SaveOTP: %v", err)
	}
	if _, err := otp.TakeCode(ctx, db, "h1", 1200); err == nil {
		t.Fatal("过期记录不应取到码")
	}
	// 同号收到新短信
	if err := otp.SaveOTP(ctx, db, "h1", "444444", 3000, 3300); err != nil {
		t.Fatalf("覆盖 SaveOTP: %v", err)
	}
	if code, err := otp.TakeCode(ctx, db, "h1", 3100); err != nil || code != "444444" {
		t.Errorf("覆盖过期记录后 TakeCode = (%q, %v)，期望 (\"444444\", nil)", code, err)
	}
}

// TestSaveOTPTTLHelperFromDuration 端到端：按 OTP_TTL_SECONDS 语义换算过期时间后写入可取。
func TestSaveOTPTTLHelperFromDuration(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()

	now := time.Now().Unix()
	ttl := 300 * time.Second // wrangler.jsonc 中 OTP_TTL_SECONDS 的默认值
	if err := otp.SaveOTP(ctx, db, "h1", "555555", now, now+int64(ttl/time.Second)); err != nil {
		t.Fatalf("SaveOTP: %v", err)
	}
	if code, err := otp.TakeCode(ctx, db, "h1", now); err != nil || code != "555555" {
		t.Errorf("TTL 窗口内 TakeCode = (%q, %v)，期望 (\"555555\", nil)", code, err)
	}
	// TTL 窗口外不可取（expires_at == now 即视为过期，与生产 WHERE 条件一致）
	if _, err := otp.TakeCode(ctx, db, "h1", now+int64(ttl/time.Second)); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("超过 TTL 后 err = %v，期望 sql.ErrNoRows", err)
	}
}
