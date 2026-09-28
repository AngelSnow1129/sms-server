//go:build js && wasm
// +build js,wasm

// SMSServer Cloudflare 版入口：Workers + D1。
//
// 与自托管版的关键差异：Workers 实例间不共享内存，D1 是唯一事实源。
// 验证码写入 D1（同号新码覆盖旧码）；读取用单条 UPDATE ... RETURNING 原子地把
// pending 翻成 read 并返回验证码，记录软删除保留 10 分钟供对账，由 Cron 定期物理清理。
package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/syumai/workers-go"
	"github.com/syumai/workers-go/cloudflare"
	"github.com/syumai/workers-go/cloudflare/cron"
	_ "github.com/syumai/workers-go/cloudflare/d1" // 注册 d1 数据库驱动

	"smsserver/cloudflare/model"
	"smsserver/cloudflare/otp"
)

// 运行常量与纯逻辑集中在 otp 包（不依赖 Workers 运行时，可在 linux/amd64 下单测）。
// 这里做别名转发，保持本文件内部写法不变。
const (
	webhookPathPrefix    = otp.WebhookPathPrefix
	maxBodyBytes         = otp.MaxBodyBytes
	readRetentionSeconds = otp.ReadRetentionSeconds
	tokenLogPrefixLen    = otp.TokenLogPrefixLen
)

// defaultOTPTTL 验证码默认有效期（OTP_TTL_SECONDS 未配置或非法时）
const defaultOTPTTL = 5 * time.Minute

// handler HTTP 处理器
type handler struct {
	db *sql.DB
}

// config 单个事件使用的运行配置。
//
// 为什么不在 handler 上缓存：cfvendor 生成的 worker.mjs 为每个事件（fetch / scheduled）
// 都新建一个 WebAssembly 实例并重新执行 main()，实测包级计数器在连续请求中始终为 1，
// 即 handler 字段实际上并不跨请求共享，缓存与否都不影响正确性。
// 但「每事件新建实例」属于运行时实现细节而非 Go 层保证，因此这里改为每请求从运行时
// 读取一次，不保留可变共享状态：既不会读到过期密钥，也不依赖上述实现细节。
type config struct {
	webhookSecret string
	hmacSecret    string
	otpTTL        time.Duration
}

// loadConfig 从运行时上下文读取 secrets 与 vars。
// cloudflare.Getenv 依赖请求运行时上下文，在 main 中调用会 panic（见 cloudflare/env.go），
// 因此只能在 handler / cron 任务内调用。
// 注意：GetOTP 只按 token_hash 查表，不需要任何 secret，故不调用本函数。
// Cron 清理只按时间条件删除，也不需要 secrets。
func loadConfig() config {
	cfg := config{otpTTL: defaultOTPTTL}
	cfg.webhookSecret = cloudflare.Getenv("WEBHOOK_SECRET")
	cfg.hmacSecret = cloudflare.Getenv("HMAC_SECRET")
	if sec, err := strconv.Atoi(cloudflare.Getenv("OTP_TTL_SECONDS")); err == nil && sec > 0 {
		cfg.otpTTL = time.Duration(sec) * time.Second
	}
	return cfg
}

func main() {
	// D1 连接按 worker 生命周期复用；驱动内部按请求上下文执行查询
	db, err := sql.Open("d1", "DB")
	if err != nil {
		panic("open d1: " + err.Error())
	}
	h := &handler{db: db}

	// 同时注册 HTTP 与 Cron 两个触发器，就绪后等待运行时完成。
	//
	// 为什么用 NonBlock + 手动 Ready：两个触发器都要在同一个 Go 实例里注册，
	// 而 workers.Serve() / cron.ScheduleTask() 这两个阻塞版本各自都会调用 workers.Ready()
	// 并阻塞在 <-Done() 上（见 handler_js.go:91-95、cron/scheduler.go:54-58），
	// 不能用两次。取 NonBlock 版本注册后只调用一次 Ready()，语义与 Serve() 等价。
	//
	// 为什么 select 两个 Done：Ready() 只是通知 JS 侧「handler 已注册」，main 若直接返回，
	// Go 程序退出会让实例被销毁（wasm_exec 的 runtime.wasmExit → delete this._inst），
	// 请求就再也处理不到了，所以 main 必须阻塞住。
	//   - workers.Done()（handler_js.go:115/52-59）在响应体 ReadCloser 被关闭时关闭，
	//     即一次请求处理完成后解除阻塞；
	//   - cron.Done()（cron/scheduler.go:66-69）按源码注释「为了支持 WaitUntil 永不关闭」，
	//     阻塞在它上面等价于永久阻塞。
	// 因此：HTTP 事件在响应完成后退出；Cron 事件不会让 main 提前退出（清理跑完即可）。
	// 每个事件都有独立的 Wasm 实例（worker.mjs 每次 new WebAssembly.Instance + go.run），
	// 所以 main 何时返回不会影响后续事件。
	workers.ServeNonBlock(routes(h))
	cron.ScheduleTaskNonBlock(h.cleanupExpired)
	workers.Ready()

	select {
	case <-workers.Done():
	case <-cron.Done():
	}
}

// routes 注册全部路由
func routes(h *handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(webhookPathPrefix, h.WebhookSMS)
	mux.HandleFunc("/api/v1/otp", h.GetOTP)
	// 运行配置摘要（不含密钥明文），部署后访问根路径即可核对 secrets 是否生效
	mux.HandleFunc("/", h.ConfigStatus)
	return mux
}

// ConfigStatus 返回配置摘要与路由说明，便于部署后自检
func (h *handler) ConfigStatus(w http.ResponseWriter, r *http.Request) {
	cfg := loadConfig()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":          "ok",
		"hmac_secret_set": cfg.hmacSecret != "",
		"webhook_set":     cfg.webhookSecret != "",
		"otp_ttl_seconds": int(cfg.otpTTL / time.Second),
		"endpoints": []string{
			"POST /api/v1/webhook/sms/{WEBHOOK_SECRET}",
			"POST /api/v1/otp",
		},
	})
}

// WebhookSMS 短信回调接口。
// POST /api/v1/webhook/sms/{token}，token 即 WEBHOOK_SECRET，常量时间比较。
// Workers 无常驻进程，处理改为同步：提取验证码并写入 D1 后立即返回。
func (h *handler) WebhookSMS(w http.ResponseWriter, r *http.Request) {
	cfg := loadConfig()
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token := otp.ExtractWebhookToken(r.URL.Path)
	if token == "" ||
		subtle.ConstantTimeCompare([]byte(token), []byte(cfg.webhookSecret)) != 1 {
		// 鉴权失败必须留痕，否则密钥爆破在服务端完全不可见
		log.Printf("[网关] webhook 鉴权失败 path=%q", r.URL.Path)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var req struct {
		Provider  string `json:"provider"`
		Sender    string `json:"sender"`
		Recipient string `json:"recipient"`
		Body      string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Printf("[网关] webhook 请求体解析失败 err=%v", err)
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	// 验证码提取逻辑见 otp.ExtractCode（无 Workers 运行时依赖，可单测）
	code := otp.ExtractCode(req.Body)
	if code == "" {
		// 未提取到验证码：不写入 D1，该号码的旧码保持原状（与自托管版一致）
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	tokenHash := model.HMACPhoneNumber(req.Recipient, cfg.hmacSecret)
	if err := h.saveOTP(r.Context(), tokenHash, code, cfg.otpTTL); err != nil {
		log.Printf("[数据库] 写入失败 recipient_hash=%s err=%v", tokenHash[:tokenLogPrefixLen], err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// 注意：禁止记录验证码明文与完整 token（详见根目录 docs/LOGGING.md）
	log.Printf("[短信] 处理完成 recipient_hash=%s 提取到验证码=true", tokenHash[:tokenLogPrefixLen])
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GetOTP 查询验证码：命中即失效（阅后即焚），但记录不物理删除。
// 单条 UPDATE ... RETURNING 原子地把 status 从 pending 翻成 read 并写入 read_at，
// 并发下只有一个请求能把同一 token 翻成 read（其余请求查不到 pending 行）。
// 已读记录保留 readRetentionSeconds 供对账「调用方说没收到码」，由 Cron 物理清理。
// POST /api/v1/otp {"token":"<HMAC-SHA256(手机号, HMAC_SECRET) 的十六进制>"}
// 说明：本接口只按 token_hash 查表，token 本身就是 HMAC 值，不需要任何 secret，
// 因此不调用 loadConfig（多读一次 env 只会增加无谓开销）。
func (h *handler) GetOTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Printf("[查询] 请求体解析失败 err=%v", err)
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	req.Token = strings.TrimSpace(req.Token)
	if req.Token == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}

	// 单条 UPDATE ... RETURNING 由存储层保证原子性：并发下只有一个请求能把同一
	// token 翻成 read，其余请求得到 sql.ErrNoRows。见 otp.TakeCode。
	code, err := otp.TakeCode(r.Context(), h.db, req.Token, time.Now().Unix())

	resp := model.OTPResponse{Status: "pending"}
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// 未写入 / 已过期 / 已被取走，语义与自托管版一致
	case err != nil:
		log.Printf("[数据库] 取码失败 err=%v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	default:
		resp.Status = "success"
		resp.Code = &code
	}
	writeJSON(w, http.StatusOK, resp)
}

// saveOTP 写入验证码，同号新码直接覆盖旧码（upsert，与自托管版缓存覆盖语义一致）。
// otpTTL 由调用方按当前请求的配置传入；SQL 与封装见 otp.SaveOTP。
func (h *handler) saveOTP(ctx context.Context, tokenHash, code string, otpTTL time.Duration) error {
	now := time.Now().Unix()
	return otp.SaveOTP(ctx, h.db, tokenHash, code, now, now+int64(otpTTL/time.Second))
}

// cleanupExpired Cron 任务（每 30 分钟，见 wrangler.jsonc）：
// 清理已读超过保留窗口的记录与未读但已过期的记录，保证数据不无限积累。
// SQL 与封装见 otp.CleanupExpired。
func (h *handler) cleanupExpired(ctx context.Context) error {
	nRead, nExpired, err := otp.CleanupExpired(ctx, h.db, time.Now().Unix())
	if err != nil {
		return err
	}
	log.Printf("[清理] 已读超时=%d 未读过期=%d", nRead, nExpired)
	return nil
}

// writeJSON 输出 JSON 响应
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
