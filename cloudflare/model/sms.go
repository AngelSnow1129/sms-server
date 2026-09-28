// Package model 定义 Cloudflare 版使用的数据模型与工具函数。
// HMAC 逻辑与根目录 model 包保持一致，调用方无需区分部署形态。
package model

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// OTPResponse 查询验证码响应
type OTPResponse struct {
	Status string  `json:"status"`         // 状态：success/pending
	Code   *string `json:"code,omitempty"` // 验证码
}

// HMACPhoneNumber 对手机号进行 HMAC-SHA256 哈希，返回十六进制（与自托管版一致）
func HMACPhoneNumber(phone, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(phone))
	return hex.EncodeToString(mac.Sum(nil))
}
