package handler

import (
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"smsserver/model"
)

// WebhookPathPrefix webhook 路由前缀。
//
// 注意：Go 1.21 的 http.ServeMux 不支持 `{token}` 通配语法（1.22 才引入 Routing
// Enhancements），若按 `/api/v1/webhook/sms/{token}` 注册，`{token}` 只会被当作普通
// 路径段字面量，真实密钥的请求会全部 404。因此注册前缀，由处理器自行从路径取 token。
const WebhookPathPrefix = "/api/v1/webhook/sms/"
const SMSForwardPathPrefix = "/api/v1/webhook/smsforward/"

// maxBodyBytes 请求体上限，防止未鉴权接口被超大 body 拖垮
const maxBodyBytes = 64 << 10 // 64 KiB

// otpService 定义处理器所需的业务能力，抽象为接口便于测试注入
type otpService interface {
	ProcessIncomingSMSAsync(provider, sender, recipient, body, channelID, source string, receivedAt time.Time)
	GetOTP(token string) (string, bool)
}

// Handler HTTP处理器
type Handler struct {
	svc                otpService
	webhookSecret      string
	smsForwardChannels map[string]model.SMSForwardChannel
}

// NewHandler 创建处理器实例
func NewHandler(svc otpService, webhookSecret string, smsForwardChannels map[string]model.SMSForwardChannel) *Handler {
	if smsForwardChannels == nil {
		smsForwardChannels = map[string]model.SMSForwardChannel{}
	}
	return &Handler{
		svc:                svc,
		webhookSecret:      webhookSecret,
		smsForwardChannels: smsForwardChannels,
	}
}

// NewRouter 注册全部路由。
//
// 抽成函数便于测试断言路由注册方式：Go 1.21 的 ServeMux 不支持 `{token}` 通配语法，
// 必须按前缀注册，否则真实密钥的请求会全部 404。
func NewRouter(h *Handler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc(WebhookPathPrefix, h.WebhookSMS)
	mux.HandleFunc(SMSForwardPathPrefix, h.WebhookSMSForward)
	mux.HandleFunc("/api/v1/otp", h.GetOTP)
	return mux
}

// extractWebhookToken 从请求路径中取出 webhook token。
// 前缀不匹配、token 为空、或路径含多余层级（如 /api/v1/webhook/sms/a/b）
// 时返回空串，由调用方按鉴权失败处理。
func extractWebhookToken(path string) string {
	if !strings.HasPrefix(path, WebhookPathPrefix) {
		return ""
	}
	token := strings.TrimPrefix(path, WebhookPathPrefix)
	if token == "" || strings.Contains(token, "/") {
		return ""
	}
	return token
}

// WebhookSMS 短信回调接口
// POST /api/v1/webhook/sms/{token}
func (h *Handler) WebhookSMS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 验证Webhook token
	token := extractWebhookToken(r.URL.Path)
	if token == "" ||
		subtle.ConstantTimeCompare([]byte(token), []byte(h.webhookSecret)) != 1 {
		// 鉴权失败必须留痕，否则密钥爆破在服务端完全不可见
		log.Printf("[网关] webhook 鉴权失败 path=%q remote=%s", r.URL.Path, r.RemoteAddr)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

	// 解析请求体
	var req struct {
		Provider  string `json:"provider"`
		Sender    string `json:"sender"`
		Recipient string `json:"recipient"`
		Body      string `json:"body"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Printf("[网关] webhook 请求体解析失败 remote=%s err=%v", r.RemoteAddr, err)
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	// 异步处理短信（不阻塞响应），由 service 纳入在途追踪以便优雅关闭时等待
	h.svc.ProcessIncomingSMSAsync(
		req.Provider,
		req.Sender,
		req.Recipient,
		req.Body,
		"",
		"",
		time.Now(),
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

func extractSMSForwardPath(path string) (channelID string, token string) {
	if !strings.HasPrefix(path, SMSForwardPathPrefix) {
		return "", ""
	}
	rest := strings.TrimPrefix(path, SMSForwardPathPrefix)
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", ""
	}
	return parts[0], parts[1]
}

// WebhookSMSForward smsforward 多通道回调接口
// POST /api/v1/webhook/smsforward/{channel_id}/{token}
func (h *Handler) WebhookSMSForward(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	channelID, token := extractSMSForwardPath(r.URL.Path)
	channel, ok := h.smsForwardChannels[channelID]
	if !ok || !channel.Enabled || token == "" ||
		subtle.ConstantTimeCompare([]byte(token), []byte(channel.WebhookSecret)) != 1 {
		log.Printf("[网关] smsforward 鉴权失败 path=%q remote=%s", r.URL.Path, r.RemoteAddr)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

	var req struct {
		Provider  string `json:"provider"`
		Sender    string `json:"sender"`
		Recipient string `json:"recipient"`
		Body      string `json:"body"`
		Source    string `json:"source"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Printf("[网关] smsforward 请求体解析失败 channel=%s remote=%s err=%v", channelID, r.RemoteAddr, err)
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	if len(channel.SourceWhitelist) > 0 && !containsFold(channel.SourceWhitelist, req.Source) {
		log.Printf("[网关] smsforward 来源不在白名单 channel=%s source=%q remote=%s", channelID, req.Source, r.RemoteAddr)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	provider := strings.TrimSpace(req.Provider)
	if provider == "" {
		provider = channel.Provider
	}
	if provider == "" {
		provider = "smsforward"
	}

	h.svc.ProcessIncomingSMSAsync(
		provider,
		req.Sender,
		req.Recipient,
		req.Body,
		channelID,
		req.Source,
		time.Now(),
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

// GetOTP 查询验证码接口
// POST /api/v1/otp
func (h *Handler) GetOTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

	// 解析请求体
	var req struct {
		Token string `json:"token"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Printf("[查询] 请求体解析失败 remote=%s err=%v", r.RemoteAddr, err)
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	req.Token = strings.TrimSpace(req.Token)
	if req.Token == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}

	// 从内存获取验证码（阅后即焚）
	code, ok := h.svc.GetOTP(req.Token)

	resp := model.OTPResponse{Status: "pending"}
	if ok {
		resp.Status = "success"
		resp.Code = &code
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func containsFold(items []string, target string) bool {
	target = strings.TrimSpace(target)
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item), target) {
			return true
		}
	}
	return false
}
