package config

import (
	"os"
	"strconv"
	"time"
)

// Config 应用配置结构体
type Config struct {
	ServerAddr string // 服务监听地址

	// DBDriver 短信记录的持久化后端：mysql（默认，保持历史行为）或 sqlite。
	// 它决定 main.go 是否连接 MySQL 并对 model.SMSRecord 做 AutoMigrate。
	DBDriver string

	// MySQLDSN MySQL连接字符串，仅 DBDriver=mysql 时使用
	MySQLDSN string

	// SQLitePath SQLite 库文件路径，DBDriver=sqlite 时使用。
	// 验证码（表 otps）写在这里；不设即取默认值 ./sms.db。
	SQLitePath string

	HMACSecret      string        // 手机号HMAC密钥
	WebhookSecret   string        // Webhook鉴权密钥
	OTPCacheTTL     time.Duration // 验证码缓存过期时间
	CleanupInterval time.Duration // 后台清理间隔
}

// 驱动名常量。用常量而非裸字符串，避免接线处拼错后静默回落到默认分支。
const (
	// DriverMySQL 连接 MySQL 并用 GORM 自动建表（历史行为，保持默认）
	DriverMySQL = "mysql"
	// DriverSQLite 只使用本地 SQLite 文件，不连接 MySQL、不做 AutoMigrate
	DriverSQLite = "sqlite"
)

// Load 从环境变量加载配置
func Load() *Config {
	return &Config{
		ServerAddr: getEnv("SERVER_ADDR", ":53340"),
		// 默认 mysql：不设 DB_DRIVER 时行为与接入 SQLite 前完全一致（向后兼容）
		DBDriver:        getEnv("DB_DRIVER", DriverMySQL),
		MySQLDSN:        getEnv("MYSQL_DSN", "user:password@tcp(127.0.0.1:3306)/smsdb?parseTime=true&loc=Local"),
		SQLitePath:      getEnv("SQLITE_PATH", "./sms.db"),
		HMACSecret:      getEnv("HMAC_SECRET", ""),
		WebhookSecret:   getEnv("WEBHOOK_SECRET", ""),
		OTPCacheTTL:     getMinutesEnv("OTP_CACHE_TTL_MINUTES", 5),
		CleanupInterval: getSecondsEnv("CLEANUP_INTERVAL_SECONDS", 30),
	}
}

// getEnv 获取环境变量，不存在时返回默认值
func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// getMinutesEnv 按“分钟”语义读取配置，缺省或非法时使用 fallback 分钟
func getMinutesEnv(key string, fallbackMinutes int) time.Duration {
	if n, ok := getIntEnv(key); ok {
		return time.Duration(n) * time.Minute
	}
	return time.Duration(fallbackMinutes) * time.Minute
}

// getSecondsEnv 按“秒”语义读取配置，缺省或非法时使用 fallback 秒
func getSecondsEnv(key string, fallbackSeconds int) time.Duration {
	if n, ok := getIntEnv(key); ok {
		return time.Duration(n) * time.Second
	}
	return time.Duration(fallbackSeconds) * time.Second
}

// getIntEnv 读取整数环境变量，未设置或非法时返回 ok=false
func getIntEnv(key string) (int, bool) {
	v := os.Getenv(key)
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return n, true
}
