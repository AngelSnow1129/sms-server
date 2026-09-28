package model

// OTPTemplate 定义验证码提取模板。
type OTPTemplate struct {
	ID        string   `json:"id"`
	Keywords  []string `json:"keywords"`
	CodeType  string   `json:"code_type"` // numeric / alpha / alnum
	MinLength int      `json:"min_length"`
	MaxLength int      `json:"max_length"`
	Providers []string `json:"providers"`
	Channels  []string `json:"channels"`
}
