package config

import (
	"fmt"
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

	// OTPStore 验证码存储实现：memory|sqlite|mysql，缺省（空值或 auto）按 DBDriver 推导。
	// 它只决定 store.Store 的接线，与短信记录的落库后端（DBDriver）互不绑定：
	// 短信记录始终由 DBDriver 决定，验证码由本字段决定。
	OTPStore string

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

// 验证码存储实现名常量，取值与 OTP_STORE 一致
const (
	// StoreAuto 缺省值：由 DBDriver 推导（见 ResolveOTPStore）。
	// 推导结果刻意保持历史行为——不设 OTP_STORE 时，没人会观察到存储实现的任何变化。
	StoreAuto = "auto"
	// StoreMemory 内存实现 cache.OTPCache，重启即丢失
	StoreMemory = "memory"
	// StoreSQLite 本地 SQLite 文件（表 otps），重启后未过期的码仍在
	StoreSQLite = "sqlite"
	// StoreMySQL MySQL 表 otps，复用 DBDriver=mysql 已建立的 GORM 连接
	StoreMySQL = "mysql"
)

// Load 从环境变量加载配置
func Load() *Config {
	return &Config{
		ServerAddr: getEnv("SERVER_ADDR", ":53340"),
		// 默认 mysql：不设 DB_DRIVER 时行为与接入 SQLite 前完全一致（向后兼容）
		DBDriver:   getEnv("DB_DRIVER", DriverMySQL),
		MySQLDSN:   getEnv("MYSQL_DSN", "user:password@tcp(127.0.0.1:3306)/smsdb?parseTime=true&loc=Local"),
		SQLitePath: getEnv("SQLITE_PATH", "./sms.db"),
		// 默认 auto：由 DBDriver 推导，保证不设 OTP_STORE 时行为与引入该变量前完全一致
		OTPStore:        getEnv("OTP_STORE", StoreAuto),
		HMACSecret:      getEnv("HMAC_SECRET", ""),
		WebhookSecret:   getEnv("WEBHOOK_SECRET", ""),
		OTPCacheTTL:     getMinutesEnv("OTP_CACHE_TTL_MINUTES", 5),
		CleanupInterval: getSecondsEnv("CLEANUP_INTERVAL_SECONDS", 30),
	}
}

// ResolveOTPStore 把 OTP_STORE 解析成一个确定的存储实现名（StoreMemory/StoreSQLite/StoreMySQL）。
//
// OTP_STORE 与 DB_DRIVER 是两条独立的轴：DB_DRIVER 决定短信记录落到哪里，
// OTP_STORE 决定验证码存在哪里。二者可以任意组合（例如 DB_DRIVER=mysql +
// OTP_STORE=sqlite，即短信落 MySQL、验证码落本地 SQLite 文件）。
//
// 缺省值 auto 的推导规则刻意保持历史行为，这是本项目最重要的向后兼容约束：
//   - DB_DRIVER=sqlite → sqlite（OTPCache 落到文件，与引入 OTP_STORE 前的 sqlite 模式一致）；
//   - 其余（含默认的 mysql、以及 DB_DRIVER 的任意未知值）→ memory（历史默认：验证码存内存，重启即丢失）。
//
// 已知值的非法组合（例如 OTP_STORE=mysql 但 DB_DRIVER=sqlite）返回 error：
// 那种组合需要一条 MySQL 连接，而 sqlite 模式按定义完全不接触 MySQL，
// 静默回落会让运维以为验证码已持久化、实际却是内存或失败。启动期失败优于静默降级。
func (c *Config) ResolveOTPStore() (string, error) {
	switch c.OTPStore {
	case StoreAuto:
		if c.DBDriver == DriverSQLite {
			return StoreSQLite, nil
		}
		return StoreMemory, nil

	case StoreMemory, StoreSQLite:
		return c.OTPStore, nil

	case StoreMySQL:
		// MySQL 验证码存储复用 DB_DRIVER=mysql 建立的那条 GORM 连接，
		// sqlite 模式下根本没有这条连接，只能在启动期明确报错。
		if c.DBDriver != DriverMySQL {
			return "", fmt.Errorf("OTP_STORE=%s 需要 DB_DRIVER=%s 提供的 MySQL 连接，当前 DB_DRIVER=%q",
				StoreMySQL, DriverMySQL, c.DBDriver)
		}
		return StoreMySQL, nil

	default:
		return "", fmt.Errorf("OTP_STORE=%q 非法，可选值为 %s|%s|%s|%s",
			c.OTPStore, StoreAuto, StoreMemory, StoreSQLite, StoreMySQL)
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
