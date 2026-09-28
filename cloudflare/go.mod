module smsserver/cloudflare

go 1.21.4

require github.com/syumai/workers-go v0.36.0

// 内嵌 workers-go（已精简：去除 examples/docs/e2e 等），
// 保证 Deploy to Cloudflare 按钮只克隆本子目录也能完整构建。
replace github.com/syumai/workers-go => ./cfvendor/workers-go
