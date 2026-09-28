// Package otp 包含 SMSServer Cloudflare 版中不依赖 Workers 运行时的核心逻辑：
// 验证码文本提取、webhook 路由 token 解析、运行配置常量、单一取码位（slot）标识，
// 以及 D1 上执行的 SQL 语句与其封装（TakeCode / SaveOTP / SaveFailure / CleanupReadArchive）。
//
// 单 token 模型：发送（webhook 路径）与读取（/api/v1/otp）用同一个 TOKEN 鉴权，
// TOKEN 只用于校验、不落库；全 Worker 只有一个取码位，其主键固定为非敏感的 SlotKey
// 常量。因此本包既不做 HMAC、也不持有 TOKEN——鉴权在 main 层完成，本包只管 slot 数据。
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

// SlotKey 唯一取码位的主键：固定、非敏感的常量（不存 TOKEN 本身，密钥因此不进 D1）。
// 全 Worker 只有一个取码位——任意新短信都 upsert 到这一行，读取也只读它，
// 这正是「发送和读取共用同一个 token」的最简形态：TOKEN 管鉴权，SlotKey 管数据行。
const SlotKey = "singleton"

// FailureCode 提取失败时落库到 code 列的标记值。
// 用户取码时若看到该值，说明最近一条短信未能提取出验证码（正文里没有 4-8 位数字）；
// 该记录不会被任何清理任务删除（清理只针对 status='read'），新短信到达时按 upsert 覆盖。
const FailureCode = "Failures"

// ReadArchiveWindowSeconds 已消费（read）记录的物理清理窗口，超过后由每月一次的
// Cron 批量删除。未读（pending）与失败（failed）记录不受影响，严禁自动删除。
// 窗口取 30 天：已消费记录本就允许累积，低频清理即可控量。
const ReadArchiveWindowSeconds = 30 * 24 * 3600 // 30 天

// numericCode 数字验证码兜底正则：匹配 4-8 位纯数字。
// \b 是 ASCII 词边界：中文/标点两侧都算边界，纯数字长串（如订单号）内部不算，
// 因此「订单号20240921123456，验证码为9876」只会取到 9876。
var numericCode = regexp.MustCompile(`\b(\d{4,8})\b`)

// keywordCode 关键词紧邻的验证码（优先于全局数字兜底）：
// 中文「验证码/动态码/校验码/动态密码」或英文「verification code/passcode/otp/code」，
// 后接可选连接词（为/是 或英文 is）与分隔符，再取一个 4-10 位的字母/数字串。
//
// 这一支支持纯字符串验证码，如「您的验证码为Gcfx」取 Gcfx、「验证码：Ab12Cd」取 Ab12Cd；
// 之所以要求紧邻关键词，是为了不把正文里任意英文单词当成验证码。
// 纯数字长度上限（8 位）由 validCodeToken 在匹配后校验——RE2 不支持前瞻，
// 无法在同一条正则里区分「9 位数字」与「含字母的串」。
var keywordCode = regexp.MustCompile(
	`(?i)(?:验证码|动态码|校验码|动态密码|verification[ _-]?code|passcode|otp|code)` +
		`(?:\s+is|[为是的])?[\s:：]*` +
		`([A-Za-z0-9]{4,10})\b`)

// ExtractCode 从短信内容中提取验证码，提取不到返回空串。
// 先取关键词紧邻的码（数字/字母/混合均可，例如 Gcfx），再退回全局 4-8 位数字启发式。
func ExtractCode(body string) string {
	if m := keywordCode.FindStringSubmatch(body); len(m) > 1 && validCodeToken(m[1]) {
		return m[1]
	}
	if m := numericCode.FindStringSubmatch(body); len(m) > 1 {
		return m[1]
	}
	return ""
}

// validCodeToken 校验关键词命中的候选码：纯数字限 4-8 位；含字母（纯字母或字母数字混合）限 4-10 位。
func validCodeToken(c string) bool {
	if allDigits(c) {
		return len(c) >= 4 && len(c) <= 8
	}
	return len(c) >= 4 && len(c) <= 10
}

// allDigits 判断字符串是否全为 ASCII 数字
func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
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
