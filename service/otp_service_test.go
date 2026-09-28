package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"smsserver/cache"
	"smsserver/model"
)

const testHMACSecret = "test-hmac-secret"

// fakeRepository 记录写入请求，避免测试依赖真实数据库
type fakeRepository struct {
	mu      sync.Mutex
	records []*model.SMSRecord
	err     error
}

func (f *fakeRepository) Create(record *model.SMSRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.records = append(f.records, record)
	return nil
}

func (f *fakeRepository) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.records)
}

func (f *fakeRepository) last() *model.SMSRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.records) == 0 {
		return nil
	}
	return f.records[len(f.records)-1]
}

func newTestService(repo *fakeRepository) (*OTPService, *cache.OTPCache) {
	c := cache.NewOTPCache(time.Minute)
	templates := []model.OTPTemplate{
		{ID: "cn_numeric", Keywords: []string{"验证码"}, CodeType: "numeric", MinLength: 4, MaxLength: 8},
		{ID: "en_alnum", Keywords: []string{"code", "otp", "verification code"}, CodeType: "alnum", MinLength: 4, MaxLength: 10},
	}
	return NewOTPService(repo, c, testHMACSecret, templates), c
}

// TestExtractCode 覆盖验证码提取的各类真实短信格式
func TestExtractCode(t *testing.T) {
	svc, _ := newTestService(&fakeRepository{})

	tests := []struct {
		name string
		body string
		want string
	}{
		{"中文模板短信", "【某某】您的验证码是123456，5分钟内有效。", "123456"},
		{"英文短信", "Your code is 12345", "12345"},
		{"冒号分隔", "您的验证码：123456", "123456"},
		{"紧邻无分隔", "验证码123456", "123456"},
		{"方括号前缀", "[#] 123456 is your code", "123456"},
		{"空格分隔", "尊敬的用户，您的动态密码为 1234 感谢使用", "1234"},
		{"超长数字不误取", "订单号20240921123456，验证码为9876", "9876"},
		{"取第一个匹配", "验证码111111 和 222222", "111111"},
		{"八位验证码", "您的验证码是12345678，请勿泄露", "12345678"},
		{"无验证码", "您的话费余额不足，请及时充值。", ""},
		{"空内容", "", ""},
		{"仅三位数字", "您的验证码是123", ""},
		{"仅九位数字", "订单999999999已提交", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := svc.extractCode(tt.body); got != tt.want {
				t.Errorf("extractCode(%q) = %q，期望 %q", tt.body, got, tt.want)
			}
		})
	}
}

func TestProcessIncomingSMSStoresCodeAndPersists(t *testing.T) {
	repo := &fakeRepository{}
	svc, c := newTestService(repo)

	svc.ProcessIncomingSMS("twilio", "+8613800000000", "+8613900000000", "您的验证码是123456", "", "", time.Now())

	// 缓存 key 必须是收件人号码的 HMAC
	key := model.HMACPhoneNumber("+8613900000000", testHMACSecret)
	code, ok := c.Get(key)
	if !ok || code != "123456" {
		t.Fatalf("缓存中 Get(key) = (%q, %v)，期望 (\"123456\", true)", code, ok)
	}

	if repo.count() != 1 {
		t.Fatalf("入库记录数 = %d，期望 1", repo.count())
	}
	rec := repo.last()
	if rec.Provider != "twilio" {
		t.Errorf("Provider = %q", rec.Provider)
	}
	if rec.Recipient != "+8613900000000" {
		t.Errorf("Recipient = %q", rec.Recipient)
	}
	if rec.Body != "您的验证码是123456" {
		t.Errorf("Body = %q", rec.Body)
	}
	if rec.ExtractedCode == nil || *rec.ExtractedCode != "123456" {
		t.Errorf("ExtractedCode = %v，期望 \"123456\"", rec.ExtractedCode)
	}
}

func TestProcessIncomingSMSWithoutCode(t *testing.T) {
	repo := &fakeRepository{}
	svc, c := newTestService(repo)

	svc.ProcessIncomingSMS("twilio", "+8613800000001", "+8613900000001", "您的话费余额不足", "", "", time.Now())

	key := model.HMACPhoneNumber("+8613900000001", testHMACSecret)
	if _, ok := c.Get(key); ok {
		t.Error("未提取到验证码时不应写入缓存")
	}
	if c.Len() != 0 {
		t.Errorf("缓存长度 = %d，期望 0", c.Len())
	}

	rec := repo.last()
	if rec.ExtractedCode != nil {
		t.Errorf("ExtractedCode = %v，期望 nil", *rec.ExtractedCode)
	}
	// 即使没有验证码，原始短信仍需落库
	if repo.count() != 1 {
		t.Errorf("入库记录数 = %d，期望 1", repo.count())
	}
}

// TestProcessIncomingSMSOnDBFailure 验证写库失败时的既有语义：
// 验证码仍留在缓存中可用，且不 panic。此时“验证码可取”与“记录已留存”是两件事。
func TestProcessIncomingSMSOnDBFailure(t *testing.T) {
	repo := &fakeRepository{err: errors.New("connection refused")}
	svc, c := newTestService(repo)

	svc.ProcessIncomingSMS("twilio", "+8613800000002", "+8613900000002", "验证码654321", "", "", time.Now())

	key := model.HMACPhoneNumber("+8613900000002", testHMACSecret)
	code, ok := c.Get(key)
	if !ok || code != "654321" {
		t.Fatalf("写库失败时缓存仍应可用，实际 (%q, %v)", code, ok)
	}
	if repo.count() != 0 {
		t.Errorf("入库记录数 = %d，期望 0", repo.count())
	}
}

func TestGetOTPIsOneShot(t *testing.T) {
	repo := &fakeRepository{}
	svc, _ := newTestService(repo)

	svc.ProcessIncomingSMS("twilio", "+8613800000000", "+8613900000000", "验证码123456", "", "", time.Now())
	key := model.HMACPhoneNumber("+8613900000000", testHMACSecret)

	code, ok := svc.GetOTP(key)
	if !ok || code != "123456" {
		t.Fatalf("首次 GetOTP = (%q, %v)，期望 (\"123456\", true)", code, ok)
	}

	if code, ok := svc.GetOTP(key); ok {
		t.Fatalf("第二次 GetOTP 仍返回 %q，阅后即焚失效", code)
	}
}

func TestGetOTPUnknownToken(t *testing.T) {
	svc, _ := newTestService(&fakeRepository{})

	if code, ok := svc.GetOTP("unknown"); ok {
		t.Fatalf("未知 token 应返回 false，实际 (%q, %v)", code, ok)
	}
}

// TestNewCodeOverwritesPrevious 验证同一号码重复收到短信时旧验证码失效
func TestNewCodeOverwritesPrevious(t *testing.T) {
	repo := &fakeRepository{}
	svc, _ := newTestService(repo)
	key := model.HMACPhoneNumber("+8613900000000", testHMACSecret)

	svc.ProcessIncomingSMS("twilio", "+8613800000000", "+8613900000000", "验证码111111", "", "", time.Now())
	svc.ProcessIncomingSMS("twilio", "+8613800000000", "+8613900000000", "验证码222222", "", "", time.Now())

	code, ok := svc.GetOTP(key)
	if !ok || code != "222222" {
		t.Fatalf("GetOTP = (%q, %v)，期望 (\"222222\", true)", code, ok)
	}
	if repo.count() != 2 {
		t.Errorf("入库记录数 = %d，期望 2", repo.count())
	}
}

// TestProcessIncomingSMSAsyncWaitInFlight 验证在途追踪：
// WaitInFlight 返回后，已投递的异步处理必须全部完成，否则优雅关闭会丢短信。
func TestProcessIncomingSMSAsyncWaitInFlight(t *testing.T) {
	repo := &fakeRepository{}
	svc, _ := newTestService(repo)

	for i := 0; i < 20; i++ {
		svc.ProcessIncomingSMSAsync("twilio", "+8613800000000", "+8613900000000", "验证码123456", "", "", time.Now())
	}
	svc.WaitInFlight()

	if repo.count() != 20 {
		t.Fatalf("入库记录数 = %d，期望 20（WaitInFlight 未等到全部完成）", repo.count())
	}
}

func TestWaitInFlightWithoutWork(t *testing.T) {
	svc, _ := newTestService(&fakeRepository{})

	done := make(chan struct{})
	go func() {
		svc.WaitInFlight()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("无在途任务时 WaitInFlight 不应阻塞")
	}
}

func TestStartCleanupWorkerStopsOnContextCancel(t *testing.T) {
	repo := &fakeRepository{}
	svc, c := newTestService(repo)

	ctx, cancel := context.WithCancel(context.Background())
	wait := svc.StartCleanupWorker(ctx, 10*time.Millisecond)

	// 放入一个短 TTL 之外的条目，等清理协程回收
	c.Set("k", "123456")
	time.Sleep(50 * time.Millisecond)

	cancel()
	// 取消后必须能等到协程真正退出，否则进程会先于协程结束，收尾日志丢失
	done := make(chan struct{})
	go func() {
		wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel 后清理协程未退出")
	}
}

func TestProcessIncomingSMSExtractsAlnumNearKeyword(t *testing.T) {
	repo := &fakeRepository{}
	svc, c := newTestService(repo)

	svc.ProcessIncomingSMS("smsforward", "+12025550111", "+12025550112", "Order 20240921123456. Your verification code is A1B2C3, valid for 5 minutes.", "android-main", "pixel-8", time.Now())

	key := model.HMACPhoneNumber("+12025550112", testHMACSecret)
	code, ok := c.Get(key)
	if !ok || code != "A1B2C3" {
		t.Fatalf("字母数字验证码提取失败: (%q, %v)", code, ok)
	}
	rec := repo.last()
	if rec.TemplateID == nil || *rec.TemplateID != "en_alnum" {
		t.Fatalf("TemplateID = %v，期望 en_alnum", rec.TemplateID)
	}
	if rec.ExtractionConfidence != "strong" || rec.ExtractionStatus != "template_strong_extracted" {
		t.Fatalf("提取状态异常: status=%s confidence=%s", rec.ExtractionStatus, rec.ExtractionConfidence)
	}
	if rec.ChannelID != "android-main" {
		t.Fatalf("ChannelID = %q，期望 android-main", rec.ChannelID)
	}
}

func TestProcessIncomingSMSFallsBackToNumericRegex(t *testing.T) {
	repo := &fakeRepository{}
	svc, c := newTestService(repo)

	svc.ProcessIncomingSMS("twilio", "+8613800000000", "+8613900000000", "请使用 876543 完成验证", "", "", time.Now())

	key := model.HMACPhoneNumber("+8613900000000", testHMACSecret)
	code, ok := c.Get(key)
	if !ok || code != "876543" {
		t.Fatalf("回退数字提取失败: (%q, %v)", code, ok)
	}
	rec := repo.last()
	if rec.ExtractionStatus != "fallback_extracted" || rec.ExtractionConfidence != "fallback" {
		t.Fatalf("回退提取状态异常: status=%s confidence=%s", rec.ExtractionStatus, rec.ExtractionConfidence)
	}
}
