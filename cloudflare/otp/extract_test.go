package otp

import "testing"

// TestExtractCode 覆盖验证码提取的各类真实短信格式。
// 用例与自托管版 service/otp_service_test.go 的 TestExtractCode 一致：
// 两边是同一条正则、同一「取第一个匹配」语义，行为必须保持一致。
func TestExtractCode(t *testing.T) {
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
		// 纯字符串 / 字母数字混合验证码
		{"纯字符串验证码", "您的验证码为Gcfx，该验证码只能使用一次，请勿泄露于他人。", "Gcfx"},
		{"字母数字混合", "验证码：Ab12Cd，请勿泄露。", "Ab12Cd"},
		{"英文字母码", "Your code is ABCD, do not share.", "ABCD"},
		{"英文大写冒号", "YOUR VERIFICATION CODE: WXYZ", "WXYZ"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractCode(tt.body); got != tt.want {
				t.Errorf("ExtractCode(%q) = %q，期望 %q", tt.body, got, tt.want)
			}
		})
	}
}

// TestExtractCodeExtra 补充边界用例：四/八位长度边界与常见分隔符。
func TestExtractCodeExtra(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"四位是下限", "code: 1234", "1234"},
		{"三位不足", "code: 123", ""},
		{"八位是上限", "code: 12345678", "12345678"},
		{"九位整体不匹配", "code: 123456789", ""},
		{"换行分隔", "您的验证码\n654321\n请勿泄露", "654321"},
		{"首段无码后段有码", "您好，本次登录验证码为 876543（10 分钟内有效）", "876543"},
		{"纯空白", "   \t  ", ""},
		// 以下三条记录的是「取第一个 \b 边界串」这一既有启发式的已知取舍，
		// 而非期望行为：分隔符（- / .）在 \b 规则下也是词边界，因此会被切开。
		// 与自托管版共用同一条正则，两边行为一致，这里把它固化下来防止无意改动。
		{"连字符切开后仍会命中", "tel 010-1234-5678", "1234"},
		{"小数点切开后仍会命中", "余额 12.345678 元", "345678"},
		{"纯数字长串内部不算边界", "订单号20240921123456", ""},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractCode(tt.body); got != tt.want {
				t.Errorf("ExtractCode(%q) = %q，期望 %q", tt.body, got, tt.want)
			}
		})
	}
}

// TestExtractWebhookToken 覆盖 webhook 路径 token 解析：
// 正常 token、前缀不匹配、空 token、多余层级。
func TestExtractWebhookToken(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{"正常 token", "/api/v1/webhook/sms/abc123", "abc123"},
		{"十六进制密钥", "/api/v1/webhook/sms/3f2a1b9c", "3f2a1b9c"},
		{"前缀不匹配", "/api/v1/webhook/abc123", ""},
		{"根目录", "/", ""},
		{"取码接口路径", "/api/v1/otp", ""},
		{"缺少前缀斜杠", "api/v1/webhook/sms/abc123", ""},
		{"主机前缀不匹配", "https://example.com/api/v1/webhook/sms/abc123", ""},
		{"空 token", "/api/v1/webhook/sms/", ""},
		{"多余层级", "/api/v1/webhook/sms/a/b", ""},
		{"末尾多余斜杠", "/api/v1/webhook/sms/abc/", ""},
		{"大小写敏感", "/API/V1/WEBHOOK/SMS/abc123", ""},
		{"空路径", "", ""},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractWebhookToken(tt.path); got != tt.want {
				t.Errorf("ExtractWebhookToken(%q) = %q，期望 %q", tt.path, got, tt.want)
			}
		})
	}
}

// TestWebhookPathPrefix 路由常量与 net/http 的前缀匹配约定一致：以 / 结尾。
func TestWebhookPathPrefix(t *testing.T) {
	const want = "/api/v1/webhook/sms/"
	if WebhookPathPrefix != want {
		t.Errorf("WebhookPathPrefix = %q，期望 %q（必须以 / 结尾才能作为 ServeMux 前缀路由）", WebhookPathPrefix, want)
	}
	if got := ExtractWebhookToken(WebhookPathPrefix + "tok"); got != "tok" {
		t.Errorf("前缀拼接后取 token = %q，期望 %q", got, "tok")
	}
}
