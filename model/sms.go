package model

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// SMSRecord 短信记录（持久化到MySQL）
//
// 复合索引 idx_recipient_time (recipient, created_at DESC) 通过索引标签声明，
// 由 AutoMigrate 幂等创建，避免手写 CREATE INDEX 的语法与重复建索引问题
// （MySQL 不支持 CREATE INDEX IF NOT EXISTS）。
type SMSRecord struct {
	ID uint64 `gorm:"primaryKey;autoIncrement"`

	// 供应商
	Provider string `gorm:"size:64;not null;default:''"`

	// 发送方号码
	Sender string `gorm:"size:128;not null;default:''"`

	// 接收方号码（复合索引首列）
	Recipient string `gorm:"size:128;not null;default:'';index:idx_recipient_time,priority:1"`

	// 原始短信内容
	Body string `gorm:"type:text;not null"`

	// 接收通道（如 smsforward 的 channel_id）
	ChannelID string `gorm:"size:64;not null;default:''"`

	// 解析出的验证码
	ExtractedCode *string `gorm:"size:32"`

	// 命中的模板 ID（未命中时为空）
	TemplateID *string `gorm:"size:64"`

	// 提取状态（如 template_strong_extracted / template_conflict / fallback_extracted / not_found）
	ExtractionStatus string `gorm:"size:64;not null;default:''"`

	// 提取置信度（strong / weak / fallback / none）
	ExtractionConfidence string `gorm:"size:16;not null;default:''"`

	// 接收时间
	ReceivedAt time.Time `gorm:"type:datetime(3);not null"`

	// 入库时间（复合索引次列，倒序）
	CreatedAt time.Time `gorm:"type:datetime(3);not null;default:CURRENT_TIMESTAMP(3);index:idx_recipient_time,sort:desc,priority:2"`
}

// TableName 指定表名
func (SMSRecord) TableName() string {
	return "sms_records"
}

// OTPInfo 内存中的验证码信息
type OTPInfo struct {
	Code     string    // 验证码
	ExpireAt time.Time // 过期时间
}

// OTPResponse 查询验证码响应
type OTPResponse struct {
	Status string  `json:"status"`         // 状态：success/pending
	Code   *string `json:"code,omitempty"` // 验证码
}

// HMACPhoneNumber 对手机号进行HMAC-SHA256哈希
func HMACPhoneNumber(phone, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(phone))
	return hex.EncodeToString(mac.Sum(nil))
}
