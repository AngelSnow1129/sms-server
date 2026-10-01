#!/bin/sh
# 交付清单验证脚本：逐项核对「cloudflare 分支 + SQLite 适配 + README 按钮」。
# 评审或接手者可直接运行本脚本取得客观证据，不依赖任何会话记录。
# 用法：./scripts/verify-deliverables.sh   （在仓库工作副本内执行）
set -u
cd "$(dirname "$0")/.."

pass=0 fail=0
ok()   { printf 'PASS  %s\n' "$1"; pass=$((pass + 1)); }
bad()  { printf 'FAIL  %s\n' "$1"; fail=$((fail + 1)); }
skip() { printf 'SKIP  %s\n' "$1"; }

echo "== 分支状态 =="
branch=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "?")
if [ "$branch" = "cloudflare" ]; then ok "当前分支 cloudflare"; else bad "当前分支为 $branch（期望 cloudflare）"; fi

echo
echo "== SQLite 适配文件 =="
for f in store/sqlite/sqlite_store.go repository/sqlite_repo.go store/mysql/mysql_store.go store/store.go; do
  if [ -s "$f" ]; then ok "$f（$(wc -l < "$f") 行）"; else bad "缺失 $f"; fi
done

echo
echo "== main.go 接线（sqlite 模式：短信 + 验证码同库落盘）=="
grep -q 'gsqlite.Dialector{Conn: sqliteDB}' main.go \
  && ok "短信归档包装共享连接池（gsqlite.Dialector{Conn}）" || bad "短信归档接线缺失"
grep -q 'sqlite.NewFromDB(sqliteDB' main.go \
  && ok "验证码存储复用共享连接池（NewFromDB）" || bad "验证码存储接线缺失"
grep -q 'SetMaxOpenConns(1)' main.go \
  && ok "SQLite 单写者连接池（MaxOpenConns=1）" || bad "连接池配置缺失"

echo
echo "== README Deploy 按钮 =="
grep -q 'deploy.workers.cloudflare.com/?url=https://github.com/AngelSnow1129/sms-server/tree/cloudflare/cloudflare' README.md \
  && ok "根 README 按钮（指向 cloudflare 分支）" || bad "根 README 按钮缺失"
grep -q 'deploy.workers.cloudflare.com' cloudflare/README.md \
  && ok "cloudflare/README 按钮" || bad "cloudflare/README 按钮缺失"

echo
echo "== 远端同步 =="
local_head=$(git rev-parse HEAD)
remote_head=$(git ls-remote origin refs/heads/cloudflare 2>/dev/null | awk '{print $1}')
if [ -n "${remote_head:-}" ] && [ "$local_head" = "$remote_head" ]; then
  ok "origin/cloudflare 与本地一致（$local_head）"
else
  bad "远端不同步 local=${local_head} remote=${remote_head:-<不可达>}"
fi

echo
echo "== Go 门禁 =="
if command -v go >/dev/null 2>&1; then
  unformatted=$(gofmt -l . 2>/dev/null | wc -l | tr -d ' ')
  if [ "$unformatted" -eq 0 ]; then ok "gofmt：无未格式化文件"; else bad "gofmt：$unformatted 个文件待格式化"; fi
  go vet ./... >/dev/null 2>&1 && ok "go vet" || bad "go vet"
  go build ./... >/dev/null 2>&1 && ok "go build" || bad "go build"
  CGO_ENABLED=0 go build ./... >/dev/null 2>&1 && ok "CGO_ENABLED=0 build（distroless 兼容）" || bad "CGO_ENABLED=0 build"
  go test -race ./cache/ ./config/ ./handler/ ./service/ ./store/ ./repository/ >/dev/null 2>&1 \
    && ok "go test -race（6 包）" || bad "go test -race"
else
  skip "go 不在 PATH，门禁跳过（仅做文件级核对）"
fi

echo
printf '合计：PASS=%d FAIL=%d\n' "$pass" "$fail"
if [ "$fail" -eq 0 ]; then echo "结论：清单全部通过"; else echo "结论：存在未通过项"; fi
exit "$fail"
