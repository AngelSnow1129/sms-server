package otp

import (
	"os"
	"strings"
	"testing"
)

// TestMainModuleHasNoSQLiteDependency 守护 Deploy to Cloudflare 的构建不被测试依赖污染。
//
// SQL 语义实证需要 SQLite 驱动，但它只能出现在 d1sqltest 那个独立模块里：
// 一旦进到主模块 go.mod，wrangler 的 npm run build（GOOS=js GOARCH=wasm go build）
// 就会把几十个间接依赖带进构建，且 Cloudflare 构建环境未必能取到。
// 本测试在普通 `go test ./...` 下运行，若有人误把驱动加到主模块即失败。
func TestMainModuleHasNoSQLiteDependency(t *testing.T) {
	b, err := os.ReadFile("../go.mod")
	if err != nil {
		t.Fatalf("读取主模块 go.mod 失败：%v", err)
	}
	mod := string(b)
	for _, banned := range []string{"sqlite", "mattn/go-sqlite3", "glebarez"} {
		if strings.Contains(mod, banned) {
			t.Errorf("主模块 go.mod 不应包含 %q（会污染 wasm 构建），内容：\n%s", banned, mod)
		}
	}
}

// TestD1SQLTestIsSeparateModule 确认 SQL 实证测试确实位于独立模块（有自己的 go.mod）。
func TestD1SQLTestIsSeparateModule(t *testing.T) {
	b, err := os.ReadFile("../d1sqltest/go.mod")
	if err != nil {
		t.Fatalf("读取 d1sqltest/go.mod 失败：%v", err)
	}
	if !strings.Contains(string(b), "module smsserver/cloudflare/d1sqltest") {
		t.Errorf("d1sqltest 应为独立模块，实际 go.mod 首行：%q", firstLine(string(b)))
	}
}

func firstLine(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		return s[:i]
	}
	return s
}
