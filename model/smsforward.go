package model

// SMSForwardChannel 表示一个 smsforward 接收通道配置。
type SMSForwardChannel struct {
	ChannelID       string   `json:"channel_id"`
	WebhookSecret   string   `json:"webhook_secret"`
	Enabled         bool     `json:"enabled"`
	Provider        string   `json:"provider"`
	TemplateIDs     []string `json:"template_ids"`
	SourceWhitelist []string `json:"source_whitelist"`
}
