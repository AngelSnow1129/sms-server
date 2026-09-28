# cloudflare 分支交付验证报告

生成时间: 2026-09-28 19:25 UTC  分支: cloudflare  HEAD: f3efd61 docs(integration): 新增 Webhook 对接指南并链接两份 README
远端: f3efd61809425879d7fa1aa6798e99d283adfaa9

## 一、清单逐项证据

| 清单项 | 证据 | 状态 |
|---|---|---|
| cloudflare 分支 | git branch = cloudflare, 5 提交已推送 origin/cloudflare | PASS |
| SQLite 适配 | store/sqlite/sqlite_store.go 244 行 (git ls-files 可查, 提交 14671cf 引入) | PASS |
| MySQL 适配 | store/mysql/mysql_store.go 282 行 (提交 fb24e36 引入, 超出原清单) | PASS |
| README 按钮 | 根 README.md 与 cloudflare/README.md 各含 Deploy 按钮, 指向 tree/cloudflare/cloudflare | PASS |
| team 工具 | 6 次调用全部 harness 层反序列化失败, 改用 task 工具等价完成 | BLOCKED(harness) |

## 二、门禁实跑输出（本报告生成时同批执行）

```
$ gofmt -l .   → 0 个未格式化文件
$ go vet ./...   → PASS
$ CGO_ENABLED=0 go build ./...   → PASS (Dockerfile 兼容)
$ go test -race -shuffle=on … → ok  	smsserver/cache	1.232s
$ go test -race -shuffle=on … → ok  	smsserver/config	1.026s
$ go test -race -shuffle=on … → ok  	smsserver/handler	1.051s
$ go test -race -shuffle=on … → ok  	smsserver/service	1.085s
$ go test -race -shuffle=on … → ok  	smsserver/store	1.567s
```

## 三、SQLite 契约测试逐用例输出

```
    --- PASS: TestStoreContract/SQLite (0.42s)
        --- PASS: TestStoreContract/SQLite/写入后可读取 (0.03s)
        --- PASS: TestStoreContract/SQLite/未写入的_token_读取为_false (0.02s)
        --- PASS: TestStoreContract/SQLite/覆盖写入后旧码失效 (0.03s)
        --- PASS: TestStoreContract/SQLite/阅后即焚只生效一次 (0.03s)
        --- PASS: TestStoreContract/SQLite/过期后不可读 (0.09s)
        --- PASS: TestStoreContract/SQLite/Delete_后不可读 (0.04s)
        --- PASS: TestStoreContract/SQLite/Cleanup_只清理过期项并保留有效项 (0.12s)
        --- PASS: TestStoreContract/SQLite/空存储_Cleanup_返回_0 (0.02s)
        --- PASS: TestStoreContract/SQLite/Len_统计未失效条目 (0.04s)
```

## 四、WASM 构建 (Cloudflare 形态)

```
GOOS=js GOARCH=wasm go build → PASS (5088491 bytes)
ok  	smsserver/cloudflare/otp	(cached)
ok  	smsserver/cloudflare/d1sqltest	(cached)
```

## 五、复现方式

```bash
git clone -b cloudflare https://github.com/AngelSnow1129/WebHookServer.git
cd WebHookServer
gofmt -l . && go vet ./... && CGO_ENABLED=0 go build ./... && go test -race ./store/...
cd cloudflare && GOOS=js GOARCH=wasm go build . && go test -race ./otp/...
```
