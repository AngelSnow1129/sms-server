package service

import (
	"context"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"smsserver/model"
	"smsserver/store"
)

// 验证码正则：匹配4-8位纯数字
var codeRegex = regexp.MustCompile(`\b(\d{4,8})\b`)
var alphaNumericRegex = regexp.MustCompile(`\b([A-Za-z0-9]{4,16})\b`)

// tokenLogPrefixLen 日志中记录的 token 前缀长度。
// identity 用完整 token 会泄漏缓存 key，截断到 8 位仍可跨日志关联同一收件人，
// 又不足以反推手机号。详见 docs/LOGGING.md。
const tokenLogPrefixLen = 8
const keywordWindow = 40

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
	templates  []model.OTPTemplate
	wg         sync.WaitGroup // 追踪在途短信处理，保证优雅关闭不丢短信
}

// NewOTPService 创建OTP服务实例
func NewOTPService(repo smsRepository, otpStore store.Store, hmacSecret string, templates []model.OTPTemplate) *OTPService {
	if len(templates) == 0 {
		templates = []model.OTPTemplate{
			{ID: "default_numeric", CodeType: "numeric", MinLength: 4, MaxLength: 8},
		}
	}
	return &OTPService{
		repo:       repo,
		store:      otpStore,
		hmacSecret: hmacSecret,
		templates:  templates,
	}
}

// ProcessIncomingSMSAsync 异步处理短信，并纳入在途追踪
func (s *OTPService) ProcessIncomingSMSAsync(provider, sender, recipient, body, channelID, source string, receivedAt time.Time) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.ProcessIncomingSMS(provider, sender, recipient, body, channelID, source, receivedAt)
	}()
}

// WaitInFlight 等待在途短信处理完成，供优雅关闭调用
func (s *OTPService) WaitInFlight() {
	s.wg.Wait()
}

// ProcessIncomingSMS 处理收到的短信
func (s *OTPService) ProcessIncomingSMS(provider, sender, recipient, body, channelID, source string, receivedAt time.Time) {
	record := &model.SMSRecord{
		Provider:             provider,
		Sender:               sender,
		Recipient:            recipient,
		Body:                 body,
		ChannelID:            channelID,
		ReceivedAt:           receivedAt,
		ExtractionStatus:     "not_found",
		ExtractionConfidence: "none",
	}

	// 生成手机号的HMAC token作为缓存key
	token := model.HMACPhoneNumber(recipient, s.hmacSecret)

	// 提取验证码（模板优先，最后回退数字规则）
	match := s.extractCodeWithTemplates(body, provider, channelID)
	code := match.Code
	if code != "" {
		record.ExtractedCode = &code
		if match.TemplateID != "" {
			record.TemplateID = &match.TemplateID
		}
		record.ExtractionStatus = match.Status
		record.ExtractionConfidence = match.Confidence
		s.store.Set(token, code)
	} else {
		record.ExtractionStatus = match.Status
		record.ExtractionConfidence = match.Confidence
	}

	// 写入数据库（失败仅记录日志，不重试）
	dbErr := s.repo.Create(record)
	if dbErr != nil {
		log.Printf("[数据库] 插入失败: recipient_hash=%s err=%v", token[:tokenLogPrefixLen], dbErr)
	}

	// 注意：禁止记录验证码明文与完整 token，详见 docs/LOGGING.md
	log.Printf("[短信] 处理完成 recipient_hash=%s channel=%s source=%s provider=%s 提取到验证码=%t 模板=%s 状态=%s 置信度=%s 已入库=%t",
		token[:tokenLogPrefixLen], channelID, source, provider, code != "", match.TemplateID, record.ExtractionStatus, record.ExtractionConfidence, dbErr == nil)
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

type extractionMatch struct {
	Code       string
	TemplateID string
	Status     string
	Confidence string
}

func (s *OTPService) extractCodeWithTemplates(body, provider, channelID string) extractionMatch {
	strongTemplates := make([]model.OTPTemplate, 0, len(s.templates))
	weakTemplates := make([]model.OTPTemplate, 0, len(s.templates))

	for _, tpl := range s.templates {
		if len(tpl.Keywords) > 0 {
			weakTemplates = append(weakTemplates, tpl)
			if templateMatchesProviderAndChannel(tpl, provider, channelID) {
				strongTemplates = append(strongTemplates, tpl)
			}
		}
	}

	if m := s.matchByTemplates(body, strongTemplates, true); m.Status != "not_found" {
		return m
	}
	if m := s.matchByTemplates(body, weakTemplates, true); m.Status != "not_found" {
		return m
	}

	fallback := s.extractCode(body)
	if fallback != "" {
		return extractionMatch{
			Code:       fallback,
			TemplateID: "fallback_numeric_regex",
			Status:     "fallback_extracted",
			Confidence: "fallback",
		}
	}
	return extractionMatch{
		Status:     "not_found",
		Confidence: "none",
	}
}

func templateMatchesProviderAndChannel(tpl model.OTPTemplate, provider, channelID string) bool {
	if len(tpl.Providers) > 0 && !containsFold(tpl.Providers, provider) {
		return false
	}
	if len(tpl.Channels) > 0 && !containsFold(tpl.Channels, channelID) {
		return false
	}
	return true
}

func (s *OTPService) matchByTemplates(body string, templates []model.OTPTemplate, requireKeyword bool) extractionMatch {
	bodyLower := strings.ToLower(body)
	for _, tpl := range templates {
		if requireKeyword && len(tpl.Keywords) == 0 {
			continue
		}
		candidates := findTemplateCandidates(body, bodyLower, tpl, requireKeyword)
		if len(candidates) == 0 {
			continue
		}
		if len(candidates) > 1 {
			return extractionMatch{
				TemplateID: tpl.ID,
				Status:     "template_conflict",
				Confidence: "none",
			}
		}
		status := "template_weak_extracted"
		confidence := "weak"
		if requireKeyword {
			status = "template_strong_extracted"
			confidence = "strong"
		}
		return extractionMatch{
			Code:       candidates[0],
			TemplateID: tpl.ID,
			Status:     status,
			Confidence: confidence,
		}
	}
	return extractionMatch{
		Status:     "not_found",
		Confidence: "none",
	}
}

func findTemplateCandidates(body, bodyLower string, tpl model.OTPTemplate, requireKeyword bool) []string {
	segments := make([]string, 0, 4)
	if requireKeyword {
		for _, kw := range tpl.Keywords {
			kw = strings.TrimSpace(kw)
			if kw == "" {
				continue
			}
			kwLower := strings.ToLower(kw)
			start := 0
			for {
				idx := strings.Index(bodyLower[start:], kwLower)
				if idx < 0 {
					break
				}
				absStart := start + idx
				absEnd := absStart + len(kwLower)
				segStart := absStart - keywordWindow
				if segStart < 0 {
					segStart = 0
				}
				segEnd := absEnd + keywordWindow
				if segEnd > len(body) {
					segEnd = len(body)
				}
				segments = append(segments, body[segStart:segEnd])
				start = absEnd
			}
		}
	} else {
		segments = append(segments, body)
	}

	unique := map[string]struct{}{}
	result := make([]string, 0, 2)
	for _, segment := range segments {
		candidate := extractByType(segment, tpl.CodeType, tpl.MinLength, tpl.MaxLength)
		if candidate == "" {
			continue
		}
		if _, ok := unique[candidate]; ok {
			continue
		}
		unique[candidate] = struct{}{}
		result = append(result, candidate)
	}
	return result
}

func extractByType(segment, codeType string, minLen, maxLen int) string {
	if minLen <= 0 {
		minLen = 4
	}
	if maxLen <= 0 || maxLen < minLen {
		maxLen = 8
	}

	switch strings.ToLower(codeType) {
	case "alpha":
		re := regexp.MustCompile(`\b([A-Za-z]{` + intRange(minLen, maxLen) + `})\b`)
		m := re.FindStringSubmatch(segment)
		if len(m) > 1 {
			return m[1]
		}
	case "alnum":
		matches := alphaNumericRegex.FindAllStringSubmatch(segment, -1)
		for _, m := range matches {
			if len(m) < 2 {
				continue
			}
			v := m[1]
			if len(v) < minLen || len(v) > maxLen {
				continue
			}
			if hasLetter(v) && hasDigit(v) {
				return v
			}
		}
	default:
		re := regexp.MustCompile(`\b(\d{` + intRange(minLen, maxLen) + `})\b`)
		m := re.FindStringSubmatch(segment)
		if len(m) > 1 {
			return m[1]
		}
	}
	return ""
}

func intRange(minLen, maxLen int) string {
	return strconv.Itoa(minLen) + "," + strconv.Itoa(maxLen)
}

func hasLetter(s string) bool {
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			return true
		}
	}
	return false
}

func hasDigit(s string) bool {
	for _, c := range s {
		if c >= '0' && c <= '9' {
			return true
		}
	}
	return false
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
