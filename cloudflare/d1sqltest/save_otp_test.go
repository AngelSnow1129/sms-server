package d1sqltest

import (
	"context"
	"testing"

	"smsserver/cloudflare/otp"
)

// TestSaveOTPUpsertOverridesOldCode 实证 upsert 语义：同 token 新码覆盖旧码，
// 且覆盖后 status 回到 pending、read_at 置 NULL（已被取走的旧码不影响新码可取），
// raw_content 被最新短信原文整体覆盖。
func TestSaveOTPUpsertOverridesOldCode(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()

	// 首次写入
	if err := otp.SaveOTP(ctx, db, "h1", "111111", "您的验证码是111111。", 1000, 1300); err != nil {
		t.Fatalf("首次 SaveOTP: %v", err)
	}
	// h2：另一号码，用于验证 upsert 不影响其他记录
	if err := otp.SaveOTP(ctx, db, "h2", "222222", "code 222222", 1000, 9_999_999_999); err != nil {
		t.Fatalf("写入另一个 token 失败：%v", err)
	}

	// 取走 h1：status=read, read_at=1050
	if taken, err := otp.TakeCode(ctx, db, "h1", 1050); err != nil || taken.Code != "111111" {
		t.Fatalf("TakeCode(h1) = (%q, %v)，期望 (\"111111\", nil)", taken.Code, err)
	}

	// 同号码收到新短信：upsert 覆盖
	if err := otp.SaveOTP(ctx, db, "h1", "333333", "新短信：验证码为333333", 2000, 2300); err != nil {
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
		t.Errorf("覆盖后 expires_at = %d，期望 2300（信息字段继续写入）", r.expiresAt)
	}
	if r.rawContent != "新短信：验证码为333333" {
		t.Errorf("覆盖后 raw_content = %q，期望最新短信原文", r.rawContent)
	}
	if r.updatedAt != 2000 {
		t.Errorf("覆盖后 updated_at = %d，期望 2000", r.updatedAt)
	}
	if r.createdAt != 1000 {
		t.Errorf("覆盖后 created_at = %d，期望保留首次写入时间 1000", r.createdAt)
	}

	// 覆盖后可再次取到新码，且原文完整返回
	taken, err := otp.TakeCode(ctx, db, "h1", 2100)
	if err != nil || taken.Code != "333333" {
		t.Errorf("覆盖后 TakeCode = (%q, %v)，期望 (\"333333\", nil)", taken.Code, err)
	}
	if taken.RawContent != "新短信：验证码为333333" {
		t.Errorf("取码返回 RawContent = %q，期望完整存储的短信原文", taken.RawContent)
	}

	// 其他 token 不受影响
	if taken, err := otp.TakeCode(ctx, db, "h2", 2100); err != nil || taken.Code != "222222" {
		t.Errorf("TakeCode(h2) = (%q, %v)，期望 (\"222222\", nil)", taken.Code, err)
	}
}

// TestSaveOTPExpiredRowStillRetrievable 实证留存需求的核心行为变更：
// 未读（pending）验证码严禁自动删除或过期失效——expires_at 仅是信息字段。
// 旧语义「过期即取不到」被明确推翻：只要未被消费、未被同号新码覆盖，
// 无论距离 expires_at 多久，都能取到。
func TestSaveOTPExpiredRowStillRetrievable(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()

	if err := otp.SaveOTP(ctx, db, "h1", "111111", "验证码111111", 1000, 1100); err != nil {
		t.Fatalf("SaveOTP: %v", err)
	}

	// 边界：expires_at == now 仍可取（取码语句不再判断 expires_at）
	if taken, err := otp.TakeCode(ctx, db, "h1", 1100); err != nil || taken.Code != "111111" {
		t.Fatalf("expires_at == now 时 TakeCode = (%q, %v)，期望 (\"111111\", nil)", taken.Code, err)
	}

	// 远在 expires_at 之后（一年后）仍可取
	if err := otp.SaveOTP(ctx, db, "h2", "222222", "验证码222222", 1000, 1500); err != nil {
		t.Fatalf("SaveOTP(h2): %v", err)
	}
	var oneYearLater int64 = 1000 + 365*24*3600
	if taken, err := otp.TakeCode(ctx, db, "h2", oneYearLater); err != nil || taken.Code != "222222" {
		t.Fatalf("过期一年后 TakeCode = (%q, %v)，期望 (\"222222\", nil)（未读严禁自动过期）", taken.Code, err)
	}
	if r := fetch(t, db, "h2"); r.status != "read" {
		t.Errorf("取走后 status = %q，期望 read（消费仍是唯一的失效途径）", r.status)
	}
}

// TestSaveFailureUpsert 实证失败标记语义：
//   - 提取失败落 failed 行（code=Failures、原文保留），不覆盖 created_at；
//   - failed 行可被同号新短信（成功或再次失败）按 upsert 覆盖；
//   - 全程不产生第二行。
func TestSaveFailureUpsert(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()

	if err := otp.SaveOTP(ctx, db, "h1", "123456", "验证码123456", 1000, 1300); err != nil {
		t.Fatalf("SaveOTP: %v", err)
	}
	// 同号再收到一条提取不到码的短信
	if err := otp.SaveFailure(ctx, db, "h1", "尊敬的客户，今日天气晴。", 2000); err != nil {
		t.Fatalf("SaveFailure: %v", err)
	}

	r := fetch(t, db, "h1")
	if r.status != "failed" {
		t.Errorf("失败标记后 status = %q，期望 failed", r.status)
	}
	if r.code != otp.FailureCode {
		t.Errorf("失败标记后 code = %q，期望 %q", r.code, otp.FailureCode)
	}
	if r.rawContent != "尊敬的客户，今日天气晴。" {
		t.Errorf("失败标记后 raw_content = %q，期望失败短信原文（完整保留）", r.rawContent)
	}
	if r.readAt.Valid {
		t.Errorf("失败标记后 read_at = %d，期望 NULL", r.readAt.Int64)
	}
	if r.createdAt != 1000 {
		t.Errorf("失败标记后 created_at = %d，期望保留首次写入时间 1000", r.createdAt)
	}
	if n := count(t, db); n != 1 {
		t.Errorf("失败标记后行数 = %d，期望 1（upsert 不产生新行）", n)
	}

	// failed 行不能被取码语句取走（WHERE status='pending'）
	if _, err := otp.TakeCode(ctx, db, "h1", 2100); err == nil {
		t.Error("failed 行不应被 TakeCode 取走")
	}

	// 同号新短信（提取成功）覆盖失败标记，恢复 pending
	if err := otp.SaveOTP(ctx, db, "h1", "654321", "验证码654321", 3000, 3300); err != nil {
		t.Fatalf("覆盖 SaveOTP: %v", err)
	}
	r = fetch(t, db, "h1")
	if r.status != "pending" || r.code != "654321" {
		t.Errorf("新短信覆盖后 = (status=%q, code=%q)，期望 (pending, 654321)", r.status, r.code)
	}
	if taken, err := otp.TakeCode(ctx, db, "h1", 3100); err != nil || taken.Code != "654321" {
		t.Errorf("覆盖后 TakeCode = (%q, %v)，期望 (\"654321\", nil)", taken.Code, err)
	}
}

// TestSaveOTPRawContentStoredVerbatim 实证 raw_content 完整保留：
// 含中文、换行、引号、超长内容的原文逐字节落库并随取码原样返回，无清洗截断。
func TestSaveOTPRawContentStoredVerbatim(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()

	raw := "【某某银行】您的验证码是 481516，5 分钟内有效，请勿泄露。\n\"quote\" 'single' — 第二行包含订单号 999999999 与金额 1234.56。"
	if err := otp.SaveOTP(ctx, db, "h1", "481516", raw, 1000, 1300); err != nil {
		t.Fatalf("SaveOTP: %v", err)
	}

	if r := fetch(t, db, "h1"); r.rawContent != raw {
		t.Errorf("落库 raw_content 与原文不一致：\n落库=%q\n原文=%q", r.rawContent, raw)
	}
	if taken, err := otp.TakeCode(ctx, db, "h1", 1100); err != nil || taken.RawContent != raw {
		t.Errorf("取码返回原文与落库不一致：err=%v 返回=%q 原文=%q", err, taken.RawContent, raw)
	}
}
