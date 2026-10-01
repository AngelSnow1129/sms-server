#!/bin/sh
# docs/ → GitHub Wiki 单向同步：仓库 docs/*.md 是唯一事实源，Wiki 是自动生成的只读镜像。
#
# 为什么是镜像而非手工编辑 Wiki：
#   - docs/ 随代码走 PR 审查与 CI 门禁，Wiki 独立仓库管不到；
#   - 手工维护两处必然漂移，镜像方式让 Wiki 永远等于 main 上的 docs/。
# 每个镜像页顶部注入「请勿在 Wiki 编辑」横幅，并把站内相对链接改写为 Wiki 链接。
#
# 设计要点（均为实测踩坑后加固，勿回退）：
#   1. 令牌不进入命令行参数：用 credential.helper 内联片段从环境变量读取，
#      避免 ps/proc 与 git 报错回显里出现令牌；远端 URL 始终是不含凭据的干净地址。
#   2. 所有 git 调用的输出都被捕获并分类，不再用 2>/dev/null 吞掉错误——
#      吞掉错误会把「推送失败」伪装成「已是最新」。
#   3. 预检区分「Wiki 未初始化(404)」与「认证/网络故障」：前者 SKIP，后者 FAIL。
#   4. 陈旧页清理：带镜像横幅但已不在 MANIFEST 里的页面会被删除；
#      无横幅的手工页面（用户在 Wiki 上自建）一律不动。
#
# 用法：
#   ./scripts/sync-wiki.sh              # 同步并推送
#   ./scripts/sync-wiki.sh --dry-run    # 只生成并展示差异，不推送
# 环境变量：
#   GH_TOKEN / GITHUB_TOKEN   有则用它认证（CI 传 secrets.GITHUB_TOKEN 即可）；
#                             本地未设置时回退到 git 自身的凭据配置（如 gh auth）
#   WIKI_REMOTE               覆盖 wiki 远端地址（自测用，如指向本地 bare 仓库）
set -u

if ! cd "$(dirname "$0")/.."; then
  printf 'FAIL  无法进入仓库根目录\n'
  exit 1
fi

REPO_SLUG="AngelSnow1129/sms-server"
WIKI_REMOTE="${WIKI_REMOTE:-https://github.com/${REPO_SLUG}.wiki.git}"
DRY_RUN=0
[ "${1:-}" = "--dry-run" ] && DRY_RUN=1

skip() { printf 'SKIP  %s\n' "$1"; }
info() { printf 'INFO  %s\n' "$1"; }
bad()  { printf 'FAIL  %s\n' "$1"; }

if ! command -v python3 >/dev/null 2>&1; then
  bad 'python3 不在 PATH，无法生成 Wiki 页面'
  exit 1
fi

TOKEN="${GH_TOKEN:-${GITHUB_TOKEN:-}}"

# 统一入口：令牌经 credential.helper 内联片段从环境读取（argv 中只有 "$GH_TOKEN" 字面量）。
# 首行 `-c credential.helper=` 清空继承来的 helper，保证行为确定（不依赖本机 gh 配置）。
git_auth() {
  if [ -n "$TOKEN" ]; then
    git -c credential.helper= \
        -c credential.helper='!f() { printf "username=x-access-token\npassword=%s\n" "$GH_TOKEN"; }; f' \
        "$@"
  else
    git "$@"
  fi
}

WORK="$(mktemp -d)" || { bad 'mktemp 失败'; exit 1; }
[ -n "$WORK" ] && [ -d "$WORK" ] || { bad 'mktemp 未返回可用目录'; exit 1; }
trap 'rm -rf "$WORK"' EXIT INT TERM

# ---- 预检：区分未初始化(404) / 认证失败 / 网络故障 ----
precheck_err="$(git_auth ls-remote "$WIKI_REMOTE" 2>&1 >/dev/null)"
if [ $? -ne 0 ]; then
  case "$precheck_err" in
    *"Repository not found"*|*"not found"*)
      # 私有仓库下「无权限」与「wiki 未初始化」都会返回 404，必须用主仓库可达性来区分：
      # 主仓库也访问不了 = 认证/网络问题，绝不能当「未初始化」静默 SKIP。
      parent_err="$(git_auth ls-remote "https://github.com/${REPO_SLUG}.git" 2>&1 >/dev/null)"
      if [ $? -ne 0 ]; then
        bad "Wiki 远端不存在，且主仓库同样不可访问——判定为认证或网络故障，而非未初始化"
        printf '%s\n' "$parent_err" | sed 's/^/      /'
        exit 1
      fi
      skip "Wiki 尚未初始化：远端 $WIKI_REMOTE 不存在（主仓库可正常访问，确认是未初始化）"
      info "一次性手工步骤：打开 https://github.com/${REPO_SLUG}/wiki 点击「创建第一个页面」（内容随意），"
      info "这一步只为让 GitHub 建出 .wiki.git 后端；随后本脚本的同步会覆盖全部内容。"
      exit 0 ;;
    *"Authentication failed"*|*"could not read Username"*|*"Permission denied"*|*"403"*)
      bad "认证失败（检查令牌是否具备仓库 contents: write 权限）"
      printf '%s\n' "$precheck_err" | sed 's/^/      /'
      exit 1 ;;
    *)
      bad "无法访问 Wiki 远端（网络或其他故障），不是「未初始化」"
      printf '%s\n' "$precheck_err" | sed 's/^/      /'
      exit 1 ;;
  esac
fi

# ---- 克隆（失败即 FAIL，不吞错误）----
clone_err="$(git_auth clone --depth 1 --quiet "$WIKI_REMOTE" "$WORK/wiki" 2>&1)"
if [ $? -ne 0 ]; then
  bad '克隆 Wiki 仓库失败'
  printf '%s\n' "$clone_err" | sed 's/^/      /'
  exit 1
fi

# ---- 生成镜像页 ----
mkdir -p "$WORK/out"
if ! python3 - "$WORK/out" "$REPO_SLUG" <<'PY'
import pathlib, re, sys

out = pathlib.Path(sys.argv[1])
docs = pathlib.Path('docs')
# 仓库 slug 由 shell 侧 REPO_SLUG 单向传入，此处不再写死（避免两处定义漂移）
REPO = sys.argv[2]
BASE = 'https://github.com/%s' % REPO

# 页面映射表（唯一权威定义）：源文件 | Wiki 页面名 | 分组 | 一句话
# 分组用于生成 _Sidebar；顺序即 Sidebar 顺序。新增维护文档务必在此登记。
MANIFEST = [
    ('README.md',                    'Home',                 '',     '索引'),
    ('LOGGING.md',                   'LOGGING',              '参考', '日志脱敏、模块前缀、字段/级别/点位规范'),
    ('INTEGRATION.md',               'INTEGRATION',          '参考', '来源方 / 取码方对接流程与 token 计算示例'),
    ('WIKI_SMSFORWARD_TEMPLATES.md', 'SMSFORWARD-TEMPLATES', '规格', '模板化提取与 SMSForward 多通道目标状态（PR #1）'),
    ('HANDOVER.md',                  'HANDOVER',             '方案', '双部署形态与两条存储链路的设计取舍'),
    ('WIKI-ARCHITECTURE.md',         'WIKI-ARCHITECTURE',    '方案', '知识库架构：载体决策、页面映射、同步机制'),
    ('PAGES-PLAN.md',                'PAGES-PLAN',           '方案', '文档站方案：信息架构、站点地址、发布与门禁'),
    ('BRANCH-VERIFICATION.md',       'BRANCH-VERIFICATION',  '记录', 'cloudflare 分支交付证据（已冻结）'),
    ('TEAM-TOOL-INCIDENT.md',        'INCIDENT-TEAM-TOOL',   '记录', 'RustCode team 工具缺陷事件复盘（已冻结）'),
]
SRC_TO_PAGE = {src: page for src, page, _, _ in MANIFEST}
BANNER_MARK = '本页由仓库自动同步生成'

# 横幅：既提示来源，也是陈旧页清理的识别标记（只有镜像页带它）
BANNER = (
    '> ⚙️ **本页由仓库自动同步生成，请勿在 Wiki 直接编辑。**\n'
    '> 修改请提 PR 到 [`%s/docs/%s`](%s/blob/main/docs/%s)，合并后由 `scripts/sync-wiki.sh` 覆盖本页。\n'
)


def rewrite(text: str) -> str:
    """把 docs/ 内的相对链接改写成 Wiki 可用的目标。"""
    # 同目录 markdown 互链 → Wiki 页面名
    def repl(md):
        target, frag = md.group(1), md.group(2) or ''
        page = SRC_TO_PAGE.get(target)
        return '](%s%s)' % (page, frag) if page else md.group(0)
    text = re.sub(r'\]\(([A-Za-z0-9_.-]+\.md)(#[^)]*)?\)', repl, text)
    # 指向仓库根的其它文件（../README.md、../CLAUDE.md、../pages/README.md 等）→ GitHub 绝对链接；
    # 注意本规则先于任何 pages/ 特判执行，故 pages/ 也会落到 blob 链接（不做文档站跳转）
    text = re.sub(r'\]\(\.\./([A-Za-z0-9_./-]+)\)', lambda m: '](%s/blob/main/%s)' % (BASE, m.group(1)), text)
    return text


written = []
for src, page, _group, _desc in MANIFEST:
    path = docs / src
    if not path.exists():
        sys.exit('缺少源文件 docs/%s' % src)
    body = rewrite(path.read_text(encoding='utf-8'))
    (out / (page + '.md')).write_text(BANNER % (REPO, src, BASE, src) + '\n' + body, encoding='utf-8')
    written.append(page)

# _Sidebar：按分组排列，Home 不重复列出
groups = {}
for src, page, group, _desc in MANIFEST:
    if group:
        groups.setdefault(group, []).append(page)
lines = ['### 文档索引', '', '[Home](Home)', '']
for group in ('参考', '规格', '方案', '记录'):
    if group in groups:
        lines.append('**%s**' % group)
        lines.append('')
        lines += ['- [%s](%s)' % (p, p) for p in groups[group]]
        lines.append('')
(out / '_Sidebar.md').write_text('\n'.join(lines).rstrip() + '\n', encoding='utf-8')

# 受管页面清单：供 shell 侧清理陈旧页与校验生成完整性
(out / '.managed').write_text('\n'.join(written + ['_Sidebar']) + '\n', encoding='utf-8')
print('生成 %d 页 + _Sidebar' % len(written))
PY
then
  bad '页面生成失败（见上方错误）'
  exit 1
fi

# ---- 覆盖式同步 ----
for f in "$WORK"/out/*.md; do
  [ -e "$f" ] || { bad '生成目录中没有页面（生成器异常）'; exit 1; }
  cp "$f" "$WORK/wiki/" || { bad "复制 $f 失败"; exit 1; }
done

# 陈旧页清理 + 生成完整性校验（只动带镜像横幅的页面，手工页一律保留）
if ! python3 - "$WORK/wiki" "$WORK/out/.managed" <<'PY'
import pathlib, sys

wiki, managed_file = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2])
MARK = '本页由仓库自动同步生成'
managed = set(managed_file.read_text(encoding='utf-8').split())

removed = []
for f in sorted(wiki.glob('*.md')):
    # managed 存的是不带扩展名的页面名，这里必须用 stem 比较（用 name 会把正常页全删掉）
    if f.stem in managed or f.name in ('_Footer.md',):
        continue
    try:
        text = f.read_text(encoding='utf-8')
    except (UnicodeDecodeError, OSError):
        continue                 # 读不了的当手工页，绝不删除
    if MARK in text:             # 是镜像页却已不在 MANIFEST → 陈旧，删除
        f.unlink()
        removed.append(f.name)
if removed:
    print('清理陈旧镜像页: %s' % ', '.join(removed))

missing = [n for n in managed if not (wiki / (n + '.md')).exists()]
if missing:
    sys.exit('生成不完整，缺少: %s' % ', '.join(missing))
PY
then
  bad '同步后校验失败'
  exit 1
fi

cd "$WORK/wiki" || { bad '无法进入克隆目录'; exit 1; }
git add -A
if git diff --cached --quiet; then
  skip 'Wiki 已是最新，无需推送'
  exit 0
fi

git -c user.name='github-actions[bot]' \
    -c user.email='41898282+github-actions[bot]@users.noreply.github.com' \
    commit -q -m 'docs: 同步 docs/ 到 Wiki（镜像）' || { bad '提交失败'; exit 1; }
info '待推送变更：'
git --no-pager show --stat --oneline HEAD | sed 's/^/      /'

if [ "$DRY_RUN" -eq 1 ]; then
  skip 'dry-run：不执行推送'
  exit 0
fi

# ---- 推送（失败即 FAIL，并输出真实错误；被并发抢先时 rebase 重试一次）----
push_err="$(git_auth push --quiet origin HEAD 2>&1)"
if [ $? -eq 0 ]; then
  printf 'PASS  Wiki 同步完成\n'
  exit 0
fi

if printf '%s' "$push_err" | grep -qiE 'non-fast-forward|rejected|fetch first'; then
  info '远端已被其它同步抢先，rebase 后重试一次'
  if git_auth pull --quiet --rebase origin HEAD >/dev/null 2>&1; then
    if git_auth push --quiet origin HEAD >/dev/null 2>&1; then
      printf 'PASS  Wiki 同步完成（rebase 重试成功）\n'
      exit 0
    fi
  fi
  bad 'rebase 重试后仍然推送失败'
else
  bad '推送失败'
fi
printf '%s\n' "$push_err" | sed 's/^/      /'
exit 1
