package d1sqltest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"

	"smsserver/cloudflare/otp"
)

// TestTakeCodeAtomicOnce 实证 CF 版最关键的并发安全保证：
// 单条 UPDATE ... RETURNING 只可能把一个 pending 行翻成 read 一次。
// 第二次取码必须得到 sql.ErrNoRows（阅后即焚），而不是同一个验证码。
func TestTakeCodeAtomicOnce(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()

	if err := otp.SaveOTP(ctx, db, "h1", "123456", "验证码123456", 1000, 1300); err != nil {
		t.Fatalf("SaveOTP: %v", err)
	}

	// 第一次：拿到验证码与原文，行被翻成 read
	taken, err := otp.TakeCode(ctx, db, "h1", 1100)
	if err != nil {
		t.Fatalf("首次 TakeCode 失败：%v", err)
	}
	if taken.Code != "123456" {
		t.Fatalf("首次 TakeCode = %q，期望 %q", taken.Code, "123456")
	}
	if taken.RawContent != "验证码123456" {
		t.Errorf("首次 TakeCode 原文 = %q，期望完整短信原文", taken.RawContent)
	}

	r := fetch(t, db, "h1")
	if r.status != "read" {
		t.Errorf("取码后 status = %q，期望 read", r.status)
	}
	if !r.readAt.Valid || r.readAt.Int64 != 1100 {
		t.Errorf("取码后 read_at = %v，期望 1100", r.readAt)
	}
	if r.updatedAt != 1100 {
		t.Errorf("取码后 updated_at = %d，期望 1100", r.updatedAt)
	}
	if r.code != "123456" {
		t.Errorf("已消费记录仍应保留验证码明文以供对账，实际 code = %q", r.code)
	}
	if r.rawContent != "验证码123456" {
		t.Errorf("已消费记录仍应保留短信原文，实际 raw_content = %q", r.rawContent)
	}

	// 第二次：已无 pending 行 → sql.ErrNoRows
	taken, err = otp.TakeCode(ctx, db, "h1", 1100)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("第二次 TakeCode err = %v (code=%q)，期望 sql.ErrNoRows", err, taken.Code)
	}
	if taken.Code != "" {
		t.Errorf("第二次 TakeCode 返回 code = %q，期望空串", taken.Code)
	}
}

// TestTakeCodeUnknownToken 未写入的 token 取码应得到 sql.ErrNoRows（不是 panic，不是空码成功）。
func TestTakeCodeUnknownToken(t *testing.T) {
	db := newDB(t)

	taken, err := otp.TakeCode(context.Background(), db, "never-written", 1000)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("未知 token err = %v (code=%q)，期望 sql.ErrNoRows", err, taken.Code)
	}
}

// 说明：旧语义「未读但已过期（expires_at <= now）取码失败」已被留存需求明确推翻——
// 未读验证码严禁自动删除或过期失效，expires_at 降级为信息字段。
// 新行为（过期一年仍可取）由 save_otp_test.go 的 TestSaveOTPExpiredRowStillRetrievable 实证。

// TestTakeCodeConcurrentExactlyOneWinner 并发实证：N 个 goroutine 同时取同一个 token，
// 恰好一个成功，其余全部 sql.ErrNoRows。这是「单条语句原子性」的直接证据。
func TestTakeCodeConcurrentExactlyOneWinner(t *testing.T) {
	const n = 50
	db := newDB(t)
	ctx := context.Background()

	if err := otp.SaveOTP(ctx, db, "hot", "999999", "验证码999999", 1000, 9_999_999_999); err != nil {
		t.Fatalf("SaveOTP: %v", err)
	}

	var (
		mu       sync.Mutex
		winners  []string
		noRows   int
		otherErr []error
		wg       sync.WaitGroup
	)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // 尽量同时发起，放大竞态窗口
			taken, err := otp.TakeCode(ctx, db, "hot", 1100)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				winners = append(winners, taken.Code)
			case errors.Is(err, sql.ErrNoRows):
				noRows++
			default:
				otherErr = append(otherErr, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(otherErr) != 0 {
		t.Fatalf("出现非 ErrNoRows 错误：%v", otherErr)
	}
	if len(winners) != 1 {
		t.Fatalf("并发取码成功次数 = %d（%v），期望恰好 1", len(winners), winners)
	}
	if winners[0] != "999999" {
		t.Errorf("赢家拿到的 code = %q，期望 %q", winners[0], "999999")
	}
	if noRows != n-1 {
		t.Errorf("ErrNoRows 次数 = %d，期望 %d", noRows, n-1)
	}
	if r := fetch(t, db, "hot"); r.status != "read" || !r.readAt.Valid {
		t.Errorf("并发后记录状态 = (status=%q, read_at=%v)，期望 (read, 非 NULL)", r.status, r.readAt)
	}
}

// TestTakeCodeConcurrentMultiConnExactlyOneWinner 同上但改用多连接 + WAL，
// 更贴近生产形态（多个 Workers 实例各有连接并发打向 D1）。
// busy_timeout 保证并发写请求排队执行而非直接 SQLITE_BUSY，
// 因此「恰好一个赢家」只能来自 WHERE status='pending' + 单条语句的原子性。
func TestTakeCodeConcurrentMultiConnExactlyOneWinner(t *testing.T) {
	const n = 50
	db := newConcurrentDB(t, 8)
	ctx := context.Background()

	if err := otp.SaveOTP(ctx, db, "hot", "888888", "验证码888888", 1000, 9_999_999_999); err != nil {
		t.Fatalf("SaveOTP: %v", err)
	}

	var (
		mu      sync.Mutex
		winners []string
		noRows  int
		other   []error
		wg      sync.WaitGroup
	)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			taken, err := otp.TakeCode(ctx, db, "hot", 1100)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				winners = append(winners, taken.Code)
			case errors.Is(err, sql.ErrNoRows):
				noRows++
			default:
				other = append(other, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(other) != 0 {
		t.Fatalf("出现非 ErrNoRows 错误（可能是锁竞争而非语义问题）：%v", other)
	}
	if len(winners) != 1 {
		t.Fatalf("多连接并发取码成功次数 = %d（%v），期望恰好 1", len(winners), winners)
	}
	if winners[0] != "888888" {
		t.Errorf("赢家拿到的 code = %q，期望 %q", winners[0], "888888")
	}
	if noRows != n-1 {
		t.Errorf("ErrNoRows 次数 = %d，期望 %d", noRows, n-1)
	}
}

// TestTakeCodeConcurrentDistinctTokensNoCrossTalk 并发下不同 token 各自只被取走一次。
func TestTakeCodeConcurrentDistinctTokensNoCrossTalk(t *testing.T) {
	const tokens = 10
	const perToken = 20
	db := newConcurrentDB(t, 8)
	ctx := context.Background()

	want := map[string]string{}
	for i := 0; i < tokens; i++ {
		tok := fmt.Sprintf("h%02d", i)
		code := fmt.Sprintf("%06d", 100000+i)
		want[tok] = code
		if err := otp.SaveOTP(ctx, db, tok, code, "验证码"+code, 1000, 9_999_999_999); err != nil {
			t.Fatalf("SaveOTP: %v", err)
		}
	}

	var (
		mu      sync.Mutex
		wins    = map[string]int{}
		other   []error
		wg      sync.WaitGroup
		startCh = make(chan struct{})
	)
	for tok := range want {
		for j := 0; j < perToken; j++ {
			tok := tok
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-startCh
				taken, err := otp.TakeCode(ctx, db, tok, 1100)
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					wins[tok]++
					if taken.Code != want[tok] {
						other = append(other, fmt.Errorf("%s 取到 %q，期望 %q", tok, taken.Code, want[tok]))
					}
				case errors.Is(err, sql.ErrNoRows):
				default:
					other = append(other, err)
				}
			}()
		}
	}
	close(startCh)
	wg.Wait()

	if len(other) != 0 {
		t.Fatalf("并发取码出现错误：%v", other)
	}
	if len(wins) != tokens {
		t.Fatalf("成功取到码的 token 数 = %d，期望 %d（ wins=%v）", len(wins), tokens, wins)
	}
	for tok, n := range wins {
		if n != 1 {
			t.Errorf("%s 被取走 %d 次，期望恰好 1 次", tok, n)
		}
	}
}

// TestTakeCodeDistinctTokensDoNotInterfere 不同 token 之间互不影响（WHERE 命中各自主键）。
func TestTakeCodeDistinctTokensDoNotInterfere(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()

	for i, c := range []string{"1111", "2222", "3333"} {
		if err := otp.SaveOTP(ctx, db, fmt.Sprintf("h%d", i), c, "验证码"+c, 1000, 9_999_999_999); err != nil {
			t.Fatalf("SaveOTP: %v", err)
		}
	}
	// 取走 h1，h0/h2 仍可取
	if _, err := otp.TakeCode(ctx, db, "h1", 1100); err != nil {
		t.Fatalf("TakeCode(h1): %v", err)
	}
	for _, tc := range []struct{ token, want string }{{"h0", "1111"}, {"h2", "3333"}} {
		taken, err := otp.TakeCode(ctx, db, tc.token, 1100)
		if err != nil || taken.Code != tc.want {
			t.Errorf("TakeCode(%s) = (%q, %v)，期望 (%q, nil)", tc.token, taken.Code, err, tc.want)
		}
	}
	if _, err := otp.TakeCode(ctx, db, "h1", 1100); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("已取走的 h1 再次取码 err = %v，期望 sql.ErrNoRows", err)
	}
}

// TestLookupState 实证取码未命中时的状态查询：区分从未写入 / 已消费 / 提取失败。
// failed 行的标记与原文必须可见（对应 /api/v1/otp 的 status=failures 响应）。
func TestLookupState(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()

	// 从未写入：Found=false，无错误
	s, err := otp.LookupState(ctx, db, "ghost")
	if err != nil {
		t.Fatalf("LookupState(ghost): %v", err)
	}
	if s.Found {
		t.Errorf("未写入 token 应 Found=false")
	}

	// 已消费
	if err := otp.SaveOTP(ctx, db, "h1", "123456", "验证码123456", 1000, 1300); err != nil {
		t.Fatalf("SaveOTP: %v", err)
	}
	if _, err := otp.TakeCode(ctx, db, "h1", 1100); err != nil {
		t.Fatalf("TakeCode: %v", err)
	}
	s, err = otp.LookupState(ctx, db, "h1")
	if err != nil || !s.Found || s.Status != "read" || s.Code != "123456" {
		t.Errorf("LookupState(h1) = (%+v, %v)，期望 (read, 123456)", s, err)
	}

	// 提取失败
	if err := otp.SaveFailure(ctx, db, "h2", "今日天气晴，无验证码。", 2000); err != nil {
		t.Fatalf("SaveFailure: %v", err)
	}
	s, err = otp.LookupState(ctx, db, "h2")
	if err != nil || !s.Found || s.Status != "failed" {
		t.Fatalf("LookupState(h2) = (%+v, %v)，期望 status=failed", s, err)
	}
	if s.Code != otp.FailureCode {
		t.Errorf("失败行 code = %q，期望 %q", s.Code, otp.FailureCode)
	}
	if s.RawContent != "今日天气晴，无验证码。" {
		t.Errorf("失败行原文 = %q，期望完整短信原文", s.RawContent)
	}
}
