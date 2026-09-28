package config

import (
	"testing"
	"time"
)

// TestLoadDefaults 验证在环境变量缺失时，默认值与 .env.example 声明的语义一致。
// 这是此前的缺陷点：TTL 变量名是“分钟”，实现却按秒解析，导致默认值为 5 秒。
func TestLoadDefaults(t *testing.T) {
	t.Setenv("SERVER_ADDR", "")
	t.Setenv("MYSQL_DSN", "")
	t.Setenv("HMAC_SECRET", "")
	t.Setenv("WEBHOOK_SECRET", "")
	t.Setenv("OTP_CACHE_TTL_MINUTES", "")
	t.Setenv("CLEANUP_INTERVAL_SECONDS", "")

	cfg := Load()

	if cfg.ServerAddr != ":53340" {
		t.Errorf("ServerAddr = %q，期望 \":53340\"", cfg.ServerAddr)
	}
	if cfg.OTPCacheTTL != 5*time.Minute {
		t.Errorf("OTPCacheTTL = %v，期望 5m0s", cfg.OTPCacheTTL)
	}
	if cfg.CleanupInterval != 30*time.Second {
		t.Errorf("CleanupInterval = %v，期望 30s", cfg.CleanupInterval)
	}
}

func TestLoadValuesFromEnv(t *testing.T) {
	t.Setenv("SERVER_ADDR", ":8080")
	t.Setenv("HMAC_SECRET", "hmac-secret")
	t.Setenv("WEBHOOK_SECRET", "webhook-secret")
	t.Setenv("OTP_CACHE_TTL_MINUTES", "5")
	t.Setenv("CLEANUP_INTERVAL_SECONDS", "30")

	cfg := Load()

	if cfg.ServerAddr != ":8080" {
		t.Errorf("ServerAddr = %q，期望 \":8080\"", cfg.ServerAddr)
	}
	if cfg.HMACSecret != "hmac-secret" {
		t.Errorf("HMACSecret = %q", cfg.HMACSecret)
	}
	if cfg.WebhookSecret != "webhook-secret" {
		t.Errorf("WebhookSecret = %q", cfg.WebhookSecret)
	}
	// 显式设置为 .env.example 中的值，结果必须与默认值一致
	if cfg.OTPCacheTTL != 5*time.Minute {
		t.Errorf("OTPCacheTTL = %v，期望 5m0s", cfg.OTPCacheTTL)
	}
	if cfg.CleanupInterval != 30*time.Second {
		t.Errorf("CleanupInterval = %v，期望 30s", cfg.CleanupInterval)
	}
}

func TestGetMinutesEnv(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		fallback int
		want     time.Duration
	}{
		{"正常分钟值", "5", 1, 5 * time.Minute},
		{"单分钟", "1", 99, time.Minute},
		{"未设置使用默认", "", 5, 5 * time.Minute},
		{"非法值回退默认", "abc", 5, 5 * time.Minute},
		{"浮点回退默认", "1.5", 5, 5 * time.Minute},
		{"零值", "0", 5, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TEST_MINUTES", tt.value)
			if got := getMinutesEnv("TEST_MINUTES", tt.fallback); got != tt.want {
				t.Errorf("getMinutesEnv(%q, %d) = %v，期望 %v", tt.value, tt.fallback, got, tt.want)
			}
		})
	}
}

func TestGetSecondsEnv(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		fallback int
		want     time.Duration
	}{
		{"正常秒值", "30", 99, 30 * time.Second},
		{"单秒", "1", 99, time.Second},
		{"未设置使用默认", "", 30, 30 * time.Second},
		{"非法值回退默认", "xyz", 30, 30 * time.Second},
		{"负值", "-5", 30, -5 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TEST_SECONDS", tt.value)
			if got := getSecondsEnv("TEST_SECONDS", tt.fallback); got != tt.want {
				t.Errorf("getSecondsEnv(%q, %d) = %v，期望 %v", tt.value, tt.fallback, got, tt.want)
			}
		})
	}
}

// TestGetMinutesEnvUnitIsMinutes 是防止单位缺陷回归的关键用例：
// 配置值 5 必须得到 5 分钟，而不是 5 秒。
func TestGetMinutesEnvUnitIsMinutes(t *testing.T) {
	t.Setenv("TEST_MINUTES", "5")

	got := getMinutesEnv("TEST_MINUTES", 5)
	if got == 5*time.Second {
		t.Fatal("回归：TTL 配置被当作秒解析（5 得到 5s），应为 5m")
	}
	if got != 5*time.Minute {
		t.Fatalf("getMinutesEnv = %v，期望 5m0s", got)
	}
}

// TestLoadOTPStoreDefaultIsAuto 锁定 OTP_STORE 的默认值。
func TestLoadOTPStoreDefaultIsAuto(t *testing.T) {
	t.Setenv("OTP_STORE", "")

	if got := Load().OTPStore; got != StoreAuto {
		t.Errorf("OTPStore = %q，期望 %q", got, StoreAuto)
	}
}

// TestResolveOTPStoreAutoKeepsLegacyBehavior 是向后兼容的关键用例：
// auto 的推导结果必须与引入 OTP_STORE 之前完全一致——
// DB_DRIVER=sqlite 走 SQLite 文件，其它（含默认 mysql）走内存。
func TestResolveOTPStoreAutoKeepsLegacyBehavior(t *testing.T) {
	tests := []struct {
		name     string
		driver   string
		otpStore string
		want     string
	}{
		{"默认 mysql + auto 走内存", DriverMySQL, StoreAuto, StoreMemory},
		{"sqlite + auto 走 SQLite", DriverSQLite, StoreAuto, StoreSQLite},
		{"显式 memory 覆盖 auto", DriverSQLite, StoreMemory, StoreMemory},
		{"显式 sqlite 覆盖 auto", DriverMySQL, StoreSQLite, StoreSQLite},
		{"显式 mysql 走 MySQL", DriverMySQL, StoreMySQL, StoreMySQL},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{DBDriver: tt.driver, OTPStore: tt.otpStore}

			got, err := cfg.ResolveOTPStore()
			if err != nil {
				t.Fatalf("ResolveOTPStore() 返回错误: %v", err)
			}
			if got != tt.want {
				t.Errorf("DB_DRIVER=%s OTP_STORE=%s => %q，期望 %q",
					tt.driver, tt.otpStore, got, tt.want)
			}
		})
	}
}

// TestResolveOTPStoreRejectsInvalid 验证非法值与不可满足的组合在启动期报错，
// 而不是静默回落到内存——静默回落会让运维以为验证码已持久化。
func TestResolveOTPStoreRejectsInvalid(t *testing.T) {
	tests := []struct {
		name     string
		driver   string
		otpStore string
	}{
		{"未知存储实现", DriverMySQL, "redis"},
		{"空字符串", DriverMySQL, ""},
		{"mysql 存储但没有 MySQL 连接", DriverSQLite, StoreMySQL},
		{"mysql 存储但 DB_DRIVER 非法", "postgres", StoreMySQL},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{DBDriver: tt.driver, OTPStore: tt.otpStore}
			if _, err := cfg.ResolveOTPStore(); err == nil {
				t.Fatalf("DB_DRIVER=%s OTP_STORE=%q 应报错，实际通过", tt.driver, tt.otpStore)
			}
		})
	}
}

// TestLoadOTPStoreFromEnv 验证 OTP_STORE 由环境变量注入，并与 DB_DRIVER 解耦：
// 短信记录后端与验证码后端是两个独立的开关。
func TestLoadOTPStoreFromEnv(t *testing.T) {
	t.Setenv("OTP_STORE", StoreMySQL)
	t.Setenv("DB_DRIVER", DriverMySQL)

	cfg := Load()
	if cfg.OTPStore != StoreMySQL {
		t.Fatalf("OTPStore = %q，期望 %q", cfg.OTPStore, StoreMySQL)
	}

	got, err := cfg.ResolveOTPStore()
	if err != nil {
		t.Fatalf("ResolveOTPStore() 返回错误: %v", err)
	}
	if got != StoreMySQL {
		t.Errorf("ResolveOTPStore() = %q，期望 %q", got, StoreMySQL)
	}
}

func TestGetEnvFallback(t *testing.T) {
	t.Setenv("TEST_STR", "")
	if got := getEnv("TEST_STR", "fallback"); got != "fallback" {
		t.Errorf("空值应回退，实际 %q", got)
	}

	t.Setenv("TEST_STR", "value")
	if got := getEnv("TEST_STR", "fallback"); got != "value" {
		t.Errorf("getEnv = %q，期望 \"value\"", got)
	}
}
