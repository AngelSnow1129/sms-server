// Package otp 包含 SMSServer Cloudflare 版中不依赖 Workers 运行时的核心逻辑：
// 验证码文本提取、webhook 路由 token 解析、运行配置常量，以及 D1 上执行的
// SQL 语句与其封装（TakeCode / SaveOTP / CleanupExpired）。
//
// 为什么单独成包：main.go 带 //go:build js && wasm，在 linux/amd64 下会被排除编译，
// 逻辑写在 package main 里就无法用普通 `go test` 覆盖。本包只依赖标准库
// （regexp / strings / database/sql），因此既能在 wasm 构建里正常编译，
// 也能在 linux/amd64 下单测，且不会把 cloudflare.Getenv 这类依赖 Workers 运行时
// 的代码（非 wasm 下不可用）带进测试路径。SQL 语句以导出常量的形式放在这里，
// 使独立测试模块可以针对「真实的语句文本」做语义实证，而不是复制一份副本。
package otp

import (
	"regexp"
	"strings"
)

// WebhookPathPrefix webhook 路由前缀，处理器从路径末段取 token（与自托管版一致）
const WebhookPathPrefix = "/api/v1/webhook/sms/"

// MaxBodyBytes 请求体上限，防止未鉴权接口被超大 body 拖垮
const MaxBodyBytes = 64 << 10 // 64 KiB

// FailureCode 提取失败时落库到 code 列的标记值。
// 用户取码时若看到该值，说明最近一条短信未能提取出验证码（正文里没有 4-8 位数字）；
// 该记录不会被任何清理任务删除（清理只针对 status='read'），同号新短信到达时按 upsert 覆盖。
const FailureCode = "Failures"

// ReadArchiveWindowSeconds 已消费（read）记录的物理清理窗口，超过后由每月一次的
// Cron 批量删除。未读（pending）与失败（failed）记录不受影响，严禁自动删除。
// 窗口取 30 天：已消费记录本就允许累积，低频清理即可控量。
const ReadArchiveWindowSeconds = 30 * 24 * 3600 // 30 天

// TokenLogPrefixLen 日志中记录的 token 前缀长度（与自托管版一致，详见 docs/LOGGING.md）
const TokenLogPrefixLen = 8

// codePattern 验证码正则：匹配 4-8 位纯数字（与自托管版相同的通用启发式）。
// \b 是 ASCII 词边界：中文/标点两侧都算边界，纯数字长串（如订单号）内部不算，
// 因此「订单号20240921123456，验证码为9876」只会取到 9876。
var codePattern = regexp.MustCompile(`\b(\d{4,8})\b`)

// ExtractCode 从短信内容中提取第一个 4-8 位数字验证码，提取不到返回空串。
// 与自托管版 service.extractCode 行为一致（同一条正则、同一「取第一个」语义）。
func ExtractCode(body string) string {
	m := codePattern.FindStringSubmatch(body)
	if len(m) > 1 {
		return m[1]
	}
	return ""
}

// ExtractWebhookToken 从请求路径末段取出 webhook token。
// 前缀不匹配、token 为空或路径含多余层级（如 /api/v1/webhook/sms/a/b）时返回空串，
// 由调用方按鉴权失败处理（与自托管版一致）。
func ExtractWebhookToken(path string) string {
	if !strings.HasPrefix(path, WebhookPathPrefix) {
		return ""
	}
	token := strings.TrimPrefix(path, WebhookPathPrefix)
	if token == "" || strings.Contains(token, "/") {
		return ""
	}
	return token
}
