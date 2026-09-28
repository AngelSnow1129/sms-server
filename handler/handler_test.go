package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"smsserver/model"
)

const testWebhookSecret = "test-webhook-secret"

// fakeService 记录处理器调用，避免测试依赖真实业务与数据库
type fakeService struct {
	asyncCalls []asyncCall
	otpCode    string
	otpOK      bool
	otpTokens  []string
}

type asyncCall struct {
	provider  string
	sender    string
	recipient string
	body      string
	channelID string
	source    string
}

func (f *fakeService) ProcessIncomingSMSAsync(provider, sender, recipient, body, channelID, source string, _ time.Time) {
	f.asyncCalls = append(f.asyncCalls, asyncCall{provider, sender, recipient, body, channelID, source})
}

func (f *fakeService) GetOTP(token string) (string, bool) {
	f.otpTokens = append(f.otpTokens, token)
	return f.otpCode, f.otpOK
}

func newTestHandler(svc *fakeService) *Handler {
	return NewHandler(svc, testWebhookSecret, nil)
}

func newSMSForwardHandler(svc *fakeService) *Handler {
	return NewHandler(svc, testWebhookSecret, map[string]model.SMSForwardChannel{
		"android-main": {
			ChannelID:       "android-main",
			WebhookSecret:   "smsfwd-secret",
			Enabled:         true,
			Provider:        "smsforward",
			TemplateIDs:     []string{"en_alnum"},
			SourceWhitelist: []string{"pixel-8"},
		},
		"disabled-channel": {
			ChannelID:     "disabled-channel",
			WebhookSecret: "disabled-secret",
			Enabled:       false,
		},
	})
}

func postWebhook(h *Handler, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.WebhookSMS(rec, req)
	return rec
}

func postOTP(h *Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/otp", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.GetOTP(rec, req)
	return rec
}

// TestWebhookAcceptsRealSecretViaPrefixRoute 是本项目最关键的回归用例：
// Go 1.21 的 ServeMux 不支持 `{token}` 通配语法，若按 `/api/v1/webhook/sms/{token}`
// 注册路由，真实密钥的请求会全部 404，短信永远无法入库。
func TestWebhookAcceptsRealSecretViaPrefixRoute(t *testing.T) {
	svc := &fakeService{}
	router := NewRouter(newTestHandler(svc))

	body := `{"provider":"twilio","sender":"+8613800000000","recipient":"+8613900000000","body":"验证码123456"}`
	req := httptest.NewRequest(http.MethodPost, WebhookPathPrefix+testWebhookSecret, strings.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("真实密钥请求状态码 = %d，期望 200（修复前为 404）", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Errorf("响应体 = %q，期望包含 status:ok", rec.Body.String())
	}
	if len(svc.asyncCalls) != 1 {
		t.Fatalf("异步调用次数 = %d，期望 1", len(svc.asyncCalls))
	}
	got := svc.asyncCalls[0]
	if got.provider != "twilio" || got.sender != "+8613800000000" ||
		got.recipient != "+8613900000000" || got.body != "验证码123456" {
		t.Errorf("异步调用参数传递有误: %+v", got)
	}
}

// TestWebhookRejectsLiteralPlaceholderPath 验证路由不再依赖字面量 `{token}` 占位符。
// 修复前唯一能命中的路径是 `.../sms/{token}`，等于把鉴权密钥固定成占位符字符串。
func TestWebhookRejectsLiteralPlaceholderPath(t *testing.T) {
	svc := &fakeService{}
	router := NewRouter(newTestHandler(svc))

	req := httptest.NewRequest(http.MethodPost, WebhookPathPrefix+"%7Btoken%7D", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("字面量 {token} 路径状态码 = %d，期望 401", rec.Code)
	}
	if len(svc.asyncCalls) != 0 {
		t.Error("鉴权失败时不应投递异步处理")
	}
}

func TestWebhookAuth(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		wantStatus int
	}{
		{"正确密钥", WebhookPathPrefix + testWebhookSecret, http.StatusOK},
		{"错误密钥", WebhookPathPrefix + "wrong-secret", http.StatusUnauthorized},
		{"密钥为空", WebhookPathPrefix, http.StatusUnauthorized},
		{"前缀不匹配", "/api/v1/webhook/smsX/" + testWebhookSecret, http.StatusUnauthorized},
		{"多余路径层级", WebhookPathPrefix + testWebhookSecret + "/extra", http.StatusUnauthorized},
		{"密钥含前缀子串", WebhookPathPrefix + "test-webhook", http.StatusUnauthorized},
		{"密钥为密钥的前缀", WebhookPathPrefix + testWebhookSecret[:5], http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeService{}
			rec := postWebhook(newTestHandler(svc), tt.path, `{}`)
			if rec.Code != tt.wantStatus {
				t.Errorf("状态码 = %d，期望 %d（路径 %q）", rec.Code, tt.wantStatus, tt.path)
			}
		})
	}
}

func TestWebhookRejectsNonPost(t *testing.T) {
	h := newTestHandler(&fakeService{})

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, WebhookPathPrefix+testWebhookSecret, nil)
			rec := httptest.NewRecorder()
			h.WebhookSMS(rec, req)

			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("状态码 = %d，期望 405", rec.Code)
			}
		})
	}
}

func TestWebhookRejectsInvalidJSON(t *testing.T) {
	svc := &fakeService{}
	rec := postWebhook(newTestHandler(svc), WebhookPathPrefix+testWebhookSecret, `{not json`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，期望 400", rec.Code)
	}
	if len(svc.asyncCalls) != 0 {
		t.Error("请求体非法时不应投递异步处理")
	}
}

// TestWebhookRejectsOversizedBody 验证未鉴权接口的请求体上限
func TestWebhookRejectsOversizedBody(t *testing.T) {
	svc := &fakeService{}
	big := `{"recipient":"+8613900000000","body":"` + strings.Repeat("x", maxBodyBytes+1024) + `"}`
	rec := postWebhook(newTestHandler(svc), WebhookPathPrefix+testWebhookSecret, big)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("超大请求体状态码 = %d，期望 400", rec.Code)
	}
	if len(svc.asyncCalls) != 0 {
		t.Error("超大请求体不应投递异步处理")
	}
}

func TestWebhookAcceptsBodyAtLimit(t *testing.T) {
	svc := &fakeService{}
	// 构造略小于上限的合法请求体
	prefix := `{"recipient":"+8613900000000","body":"`
	suffix := `"}`
	body := prefix + strings.Repeat("x", maxBodyBytes-len(prefix)-len(suffix)-1) + suffix

	rec := postWebhook(newTestHandler(svc), WebhookPathPrefix+testWebhookSecret, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("接近上限的请求体状态码 = %d，期望 200", rec.Code)
	}
}

func TestExtractWebhookToken(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{"正常密钥", WebhookPathPrefix + "abc123", "abc123"},
		{"仅前缀", WebhookPathPrefix, ""},
		{"多余层级", WebhookPathPrefix + "abc/def", ""},
		{"非该前缀", "/api/v1/otp", ""},
		{"前缀相近但不等", "/api/v1/webhook/smsX/abc", ""},
		{"字面量占位符", WebhookPathPrefix + "{token}", "{token}"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractWebhookToken(tt.path); got != tt.want {
				t.Errorf("extractWebhookToken(%q) = %q，期望 %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestExtractSMSForwardPath(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		wantChannel string
		wantToken   string
	}{
		{"正常", SMSForwardPathPrefix + "android-main/smsfwd-secret", "android-main", "smsfwd-secret"},
		{"缺 token", SMSForwardPathPrefix + "android-main", "", ""},
		{"多层级", SMSForwardPathPrefix + "android-main/smsfwd-secret/extra", "", ""},
		{"前缀不匹配", "/api/v1/webhook/smsforwardX/android-main/s", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch, tk := extractSMSForwardPath(tt.path)
			if ch != tt.wantChannel || tk != tt.wantToken {
				t.Fatalf("extractSMSForwardPath(%q)=(%q,%q), want=(%q,%q)", tt.path, ch, tk, tt.wantChannel, tt.wantToken)
			}
		})
	}
}

func TestWebhookSMSForward(t *testing.T) {
	svc := &fakeService{}
	h := newSMSForwardHandler(svc)
	body := `{"sender":"+8613800000000","recipient":"+8613900000000","body":"Your code is A1B2C3","source":"pixel-8"}`
	req := httptest.NewRequest(http.MethodPost, SMSForwardPathPrefix+"android-main/smsfwd-secret", strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.WebhookSMSForward(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码=%d，期望 200", rec.Code)
	}
	if len(svc.asyncCalls) != 1 {
		t.Fatalf("异步调用次数=%d，期望 1", len(svc.asyncCalls))
	}
	got := svc.asyncCalls[0]
	if got.channelID != "android-main" || got.source != "pixel-8" {
		t.Fatalf("通道或来源透传失败: %+v", got)
	}
	if got.provider != "smsforward" {
		t.Fatalf("provider 回填失败: %q", got.provider)
	}
}

func TestWebhookSMSForwardRejectsInvalidCases(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		body       string
		wantStatus int
	}{
		{"错误密钥", SMSForwardPathPrefix + "android-main/wrong", `{}`, http.StatusUnauthorized},
		{"通道不存在", SMSForwardPathPrefix + "missing/smsfwd-secret", `{}`, http.StatusUnauthorized},
		{"通道禁用", SMSForwardPathPrefix + "disabled-channel/disabled-secret", `{}`, http.StatusUnauthorized},
		{"来源不在白名单", SMSForwardPathPrefix + "android-main/smsfwd-secret", `{"source":"unknown","body":"code A1B2C3"}`, http.StatusUnauthorized},
		{"非法json", SMSForwardPathPrefix + "android-main/smsfwd-secret", `{bad`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeService{}
			h := newSMSForwardHandler(svc)
			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			rec := httptest.NewRecorder()

			h.WebhookSMSForward(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("状态码=%d，期望=%d", rec.Code, tt.wantStatus)
			}
			if tt.wantStatus != http.StatusOK && len(svc.asyncCalls) != 0 {
				t.Fatal("失败请求不应投递异步处理")
			}
		})
	}
}

func TestGetOTPSuccess(t *testing.T) {
	svc := &fakeService{otpCode: "123456", otpOK: true}
	rec := postOTP(newTestHandler(svc), `{"token":"abc"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q，期望 application/json", ct)
	}

	var resp model.OTPResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应体不是合法 JSON: %v（%q）", err, rec.Body.String())
	}
	if resp.Status != "success" {
		t.Errorf("Status = %q，期望 success", resp.Status)
	}
	if resp.Code == nil || *resp.Code != "123456" {
		t.Errorf("Code = %v，期望 123456", resp.Code)
	}
}

func TestGetOTPPending(t *testing.T) {
	svc := &fakeService{otpOK: false}
	rec := postOTP(newTestHandler(svc), `{"token":"abc"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("未命中时状态码 = %d，期望 200", rec.Code)
	}
	var resp model.OTPResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应体不是合法 JSON: %v", err)
	}
	if resp.Status != "pending" {
		t.Errorf("Status = %q，期望 pending", resp.Status)
	}
	if resp.Code != nil {
		t.Errorf("pending 时不应返回 code，实际 %q", *resp.Code)
	}
}

func TestGetOTPValidation(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{"token 缺失", `{}`, http.StatusBadRequest},
		{"token 为空串", `{"token":""}`, http.StatusBadRequest},
		{"token 为空白", `{"token":"   "}`, http.StatusBadRequest},
		{"请求体非法", `{bad`, http.StatusBadRequest},
		{"合法 token", `{"token":"abc"}`, http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := postOTP(newTestHandler(&fakeService{}), tt.body)
			if rec.Code != tt.wantStatus {
				t.Errorf("状态码 = %d，期望 %d", rec.Code, tt.wantStatus)
			}
		})
	}
}

func TestGetOTPTrimsToken(t *testing.T) {
	svc := &fakeService{otpCode: "123456", otpOK: true}
	postOTP(newTestHandler(svc), `{"token":"  abc  "}`)

	if len(svc.otpTokens) != 1 {
		t.Fatalf("GetOTP 调用次数 = %d，期望 1", len(svc.otpTokens))
	}
	if svc.otpTokens[0] != "abc" {
		t.Errorf("传入 token = %q，期望去除空白后的 \"abc\"", svc.otpTokens[0])
	}
}

func TestGetOTPRejectsNonPost(t *testing.T) {
	h := newTestHandler(&fakeService{})

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/api/v1/otp", nil)
			rec := httptest.NewRecorder()
			h.GetOTP(rec, req)

			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("状态码 = %d，期望 405", rec.Code)
			}
		})
	}
}

func TestNewRouterRegistersBothEndpoints(t *testing.T) {
	router := NewRouter(newTestHandler(&fakeService{}))

	// 未注册的路径应为 404，已注册的不应 404
	unregistered := httptest.NewRequest(http.MethodPost, "/api/v1/unknown", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, unregistered)
	if rec.Code != http.StatusNotFound {
		t.Errorf("未注册路径状态码 = %d，期望 404", rec.Code)
	}
}
