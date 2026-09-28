// Package model 定义 Cloudflare 版对外使用的响应模型。
// 单 token 形态下不做手机号 HMAC：TOKEN 仅用于鉴权、不落库，故此处无需哈希工具。
package model

// OTPResponse 查询验证码响应
type OTPResponse struct {
	Status     string  `json:"status"`                // 状态：success/failures/pending
	Code       *string `json:"code,omitempty"`        // 验证码（status=success 时）；status=failures 时为失败标记
	RawContent *string `json:"raw_content,omitempty"` // 短信原文（完整保留，取码成功或失败标记时返回）
	Reason     *string `json:"reason,omitempty"`      // status=failures 时的原因说明
}
