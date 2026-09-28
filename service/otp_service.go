package service

import (
	"context"
	"log"
	"regexp"
	"sync"
	"time"

	"smsserver/model"
	"smsserver/store"
)

// 验证码正则：匹配4-8位纯数字
var codeRegex = regexp.MustCompile(`\b(\d{4,8})\b`)

// tokenLogPrefixLen 日志中记录的 token 前缀长度。
// identity 用完整 token 会泄漏缓存 key，截断到 8 位仍可跨日志关联同一收件人，
// 又不足以反推手机号。详见 docs/LOGGING.md。
const tokenLogPrefixLen = 8

// smsRepository 定义业务层所需的存储能力。
// 抽象为接口便于测试替换，也避免 service 依赖具体的 GORM 实现。
type smsRepository interface {
	Create(record *model.SMSRecord) error
}

// OTPService 验证码业务逻辑
type OTPService struct {
	repo smsRepository
	// 验证码存储依赖抽象而非具体实现，便于将来替换为 SQLite 或 MySQL 实现
	store      store.Store
	hmacSecret string
	wg         sync.WaitGroup // 追踪在途短信处理，保证优雅关闭不丢短信
}

// NewOTPService 创建OTP服务实例
func NewOTPService(repo smsRepository, otpStore store.Store, hmacSecret string) *OTPService {
	return &OTPService{
		repo:       repo,
		store:      otpStore,
		hmacSecret: hmacSecret,
	}
}

// ProcessIncomingSMSAsync 异步处理短信，并纳入在途追踪
func (s *OTPService) ProcessIncomingSMSAsync(provider, sender, recipient, body string, receivedAt time.Time) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.ProcessIncomingSMS(provider, sender, recipient, body, receivedAt)
	}()
}

// WaitInFlight 等待在途短信处理完成，供优雅关闭调用
func (s *OTPService) WaitInFlight() {
	s.wg.Wait()
}

// ProcessIncomingSMS 处理收到的短信
func (s *OTPService) ProcessIncomingSMS(provider, sender, recipient, body string, receivedAt time.Time) {
	record := &model.SMSRecord{
		Provider:   provider,
		Sender:     sender,
		Recipient:  recipient,
		Body:       body,
		ReceivedAt: receivedAt,
	}

	// 生成手机号的HMAC token作为缓存key
	token := model.HMACPhoneNumber(recipient, s.hmacSecret)

	// 提取验证码
	code := s.extractCode(body)
	if code != "" {
		record.ExtractedCode = &code
		s.store.Set(token, code)
	}

	// 写入数据库（失败仅记录日志，不重试）
	dbErr := s.repo.Create(record)
	if dbErr != nil {
		log.Printf("[数据库] 插入失败: recipient_hash=%s err=%v", token[:tokenLogPrefixLen], dbErr)
	}

	// 注意：禁止记录验证码明文与完整 token，详见 docs/LOGGING.md
	log.Printf("[短信] 处理完成 recipient_hash=%s 提取到验证码=%t 已入库=%t",
		token[:tokenLogPrefixLen], code != "", dbErr == nil)
}

// GetOTP 获取验证码（阅后即焚）
func (s *OTPService) GetOTP(token string) (string, bool) {
	return s.store.GetAndDelete(token)
}

// extractCode 从短信内容中提取验证码
func (s *OTPService) extractCode(body string) string {
	matches := codeRegex.FindStringSubmatch(body)
	if len(matches) > 1 {
		return matches[1]
	}
	return ""
}

// StartCleanupWorker 启动后台清理协程，并返回一个等待其退出的函数。
//
// 调用方在取消 ctx 后必须调用返回的 wait，否则进程可能先于协程退出，
// 导致「清理已停止」这类收尾日志丢失（关闭流程不可观测）。
func (s *OTPService) StartCleanupWorker(ctx context.Context, interval time.Duration) (wait func()) {
	done := make(chan struct{})

	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		log.Printf("[清理] 已启动，间隔=%v", interval)
		for {
			select {
			case <-ctx.Done():
				log.Println("[清理] 已停止")
				return
			case <-ticker.C:
				removed := s.store.Cleanup()
				if removed > 0 {
					log.Printf("[清理] 移除=%d 剩余=%d", removed, s.store.Len())
				}
			}
		}
	}()

	return func() { <-done }
}
