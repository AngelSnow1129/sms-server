#!/bin/sh
# 文档站（pages/）一致性门禁：把 docs/PAGES-PLAN.md §6 的检查固化为可复现脚本。
# 覆盖：元数据完整性、站点地址一致性、内链有效性与锚点存在、隐私红线（零网络请求/零第三方资源）。
#
# 设计要点——**fail-closed**：任何异常（python3 缺失/崩溃、文件非 UTF-8、必需文件缺失、
# 页面目录为空）一律计为 FAIL。绝不出现「检查没跑成却报通过」的假绿。
#
# 用法：./scripts/verify-pages.sh   （在仓库工作副本内执行）
set -u
if ! cd "$(dirname "$0")/.."; then
  printf 'FAIL  无法进入仓库根目录\n'
  exit 1
fi

pass=0 fail=0
ok()  { printf 'PASS  %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL  %s\n' "$1"; fail=$((fail + 1)); }

if ! command -v python3 >/dev/null 2>&1; then
  bad 'python3 不在 PATH，文档站检查无法执行（fail-closed）'
  printf '\n合计：PASS=%d FAIL=%d\n结论：存在未通过项\n' "$pass" "$fail"
  exit 1
fi

# 检查逻辑集中在一处：python3 逐行输出 `PASS 项` / `FAIL 项`，由本脚本统一计数。
# 站点地址的单一事实源是各页 canonical；robots.txt 与 sitemap.xml 必须与之一致。
result=$(python3 - <<'PY'
import pathlib, posixpath, re, sys

root = pathlib.Path('pages')


def out(ok_flag, msg):
    print(('PASS ' if ok_flag else 'FAIL ') + msg)


def read(path):
    """读文件；非 UTF-8 或读失败一律计 FAIL（不许静默跳过）。"""
    try:
        return path.read_text(encoding='utf-8')
    except UnicodeDecodeError:
        out(False, f'{path.as_posix()} 不是合法 UTF-8（脚本无法解析其元数据）')
        return None
    except OSError as e:
        out(False, f'{path.as_posix()} 读取失败（{e}）')
        return None


if not root.is_dir():
    out(False, 'pages/ 目录不存在')
    sys.exit(0)

# 递归发现页面：子目录（如规划中的 en/）同样纳入门禁，否则会发布出未校验页面
pages = sorted(p for p in root.rglob('*.html'))
if not pages:
    out(False, 'pages/ 下没有任何 HTML 页面')
    sys.exit(0)

required_meta = {
    'canonical': '<link rel="canonical"',
    'og:title': 'property="og:title"',
    'og:url': 'property="og:url"',
    'twitter:card': 'name="twitter:card"',
    'description': 'name="description"',
    'viewport': 'name="viewport"',
    'theme-color': 'name="theme-color"',
    'favicon': 'rel="icon"',
}

texts, ids = {}, {}
unreadable = []
for p in pages:
    t = read(p)
    key = p.relative_to(root).as_posix()
    if t is None:
        unreadable.append(key)
        continue
    texts[key] = t
    ids[key] = set(re.findall(r'id="([^"]+)"', t))

# 1) 元数据完整性（含子目录页面）
missing = []
for key, t in texts.items():
    head = t.split('</head>')[0]
    for name, pat in required_meta.items():
        if pat not in head:
            missing.append(f'{key}:{name}')
out(not missing, f'元数据完整性（{len(pages)} 页 × {len(required_meta)} 项）'
    + ('' if not missing else ' 缺失：' + ', '.join(missing)))

# 2) canonical 与 og:url 逐页一致
mismatch, canonical = [], {}
for key, t in texts.items():
    head = t.split('</head>')[0]
    can = re.search(r'rel="canonical" href="([^"]+)"', head)
    ogu = re.search(r'property="og:url" content="([^"]+)"', head)
    if not (can and ogu):
        mismatch.append(f'{key}:缺少 canonical/og:url')
    elif can.group(1) != ogu.group(1):
        mismatch.append(f'{key}:{can.group(1)} != {ogu.group(1)}')
    else:
        canonical[key] = can.group(1)
out(not mismatch, 'canonical 与 og:url 一致'
    + ('' if not mismatch else ' 不一致：' + ', '.join(mismatch)))

# 3) 站点地址一致性：sitemap / robots 与页面 canonical 同源（必需文件缺失即 FAIL）
norm = lambda u: u.rstrip('/')
sitemap_path, robots_path = root / 'sitemap.xml', root / 'robots.txt'
for req in (sitemap_path, robots_path):
    if not req.exists():
        out(False, f'{req.as_posix()} 缺失（站点地址一致性无法校验）')

entries = [norm(u) for u in canonical.values() if u]
if not canonical.get('index.html'):
    out(False, '首页 canonical 缺失，站点地址单一事实源不可用')
elif entries:
    if sitemap_path.exists():
        sm = read(sitemap_path)
        locs = {norm(u) for u in re.findall(r'<loc>([^<]+)</loc>', sm or '')}
        expect = set(entries)
        out(locs == expect, 'sitemap.xml 的 <loc> 与各页 canonical 一一对应'
            + ('' if locs == expect else f' 差异：{sorted(locs ^ expect)}'))
    if robots_path.exists():
        rb = read(robots_path) or ''
        base = 'https://' + re.sub(r'^https?://', '', norm(canonical['index.html'])) + '/'
        out(f'Sitemap: {base}sitemap.xml' in rb,
            'robots.txt 的 Sitemap 指向本站 sitemap.xml')

# 4) 内链有效性：相对链接按所在目录解析，目标页与锚点都必须存在
broken = []
for key, t in texts.items():
    for h in re.findall(r'href="([^"]+)"', t):
        if h.startswith(('http://', 'https://', '//', 'mailto:', 'data:', '#')):
            continue
        m = re.match(r'([A-Za-z0-9_./-]+\.html)(#[\w-]+)?$', h, re.I)
        if not m:
            continue
        target = posixpath.normpath(posixpath.join(posixpath.dirname(key), m.group(1)))
        frag = m.group(2)
        if target not in ids:
            broken.append(f'{key}->{h}(无此页)')
        elif frag and frag[1:] not in ids[target]:
            broken.append(f'{key}->{h}(无此锚点)')
out(not broken, '站内链接与锚点全部有效'
    + ('' if not broken else ' 失效：' + ', '.join(broken)))

# 5) 隐私红线：零网络请求、零第三方资源。
#    区分「用户可点击的外链」(<a href>) 与「页面自动加载的子资源」(script/link/img/iframe/CSS url())：
#    红线只约束后者；canonical / og:url 等元数据指向本站自身，不算第三方。
#    覆盖 .html/.js/.css——CSS 与 link/img/iframe 是此前的结构性盲区。
net_tags = {'script', 'link', 'img', 'iframe', 'frame', 'source',
            'video', 'audio', 'track', 'embed', 'object', 'use'}
m0 = re.match(r'https?://[^/]+', canonical.get('index.html', '') or '')
site_origin = m0.group(0) if m0 else ''


def external(url):
    """判断子资源地址是否指向站外。相对路径、片段、data:/mailto: 一律视为站内。"""
    u = url.strip().strip('"\'')
    if not u or u.startswith(('#', 'data:', 'mailto:', 'blob:')):
        return False
    if u.startswith('//'):
        return True
    if re.match(r'https?://', u, re.I):
        return True if not site_origin else not (u.startswith(site_origin + '/') or u.rstrip('/') == site_origin)
    return False


violations = []
for f in sorted(list(root.rglob('*.html')) + list(root.rglob('*.js')) + list(root.rglob('*.css'))):
    t = read(f)
    if t is None:
        continue
    rel = f.relative_to(root).as_posix()
    for hit in ('XMLHttpRequest', 'fetch('):
        if hit in t:
            violations.append(f'{rel}:{hit}')
    suffix = f.suffix.lower()
    if suffix == '.html':
        for tag, attrs in re.findall(r'<([A-Za-z][\w:-]*)([^>]*)>', t):
            if tag.lower() not in net_tags:
                continue
            for attr, val in re.findall(r'\b(src|href|data|srcset)\s*=\s*["\']([^"\']*)["\']', attrs, re.I):
                urls = [p.strip().split(' ')[0] for p in val.split(',')] if attr.lower() == 'srcset' else [val]
                for u in urls:
                    if external(u):
                        violations.append(f'{rel}:<{tag} {attr}> 指向站外 {u[:40]}')
    if suffix == '.css':
        cands = re.findall(r'url\(\s*["\']?([^"\')]+)', t) + re.findall(r'@import\s+["\']([^"\']+)', t)
        for u in cands:
            if external(u):
                violations.append(f'{rel}:CSS 引用站外 {u.strip()[:40]}')
out(not violations, '隐私红线：无网络请求、无第三方资源（含 CSS 与 link/img/iframe）'
    + ('' if not violations else ' 违规：' + '; '.join(violations)))

if unreadable:
    out(False, '存在无法解析的页面：' + ', '.join(unreadable))
PY
)
rc=$?

# python3 崩溃 / 被信号终止时，上面的输出可能残缺；此处必须显式计为失败（fail-closed）
if [ "$rc" -ne 0 ]; then
  bad "检查脚本异常退出（python3 退出码 $rc），无论已输出何内容一律判定失败"
fi

# `grep -c` 无匹配会打印 0 但退出码为 1，故显式吞掉其退出码（本脚本无 set -e，仍写之以防将来被改坏）
pass=$(printf '%s\n' "$result" | grep -c '^PASS ' || true)
fail=$(printf '%s\n' "$result" | grep -c '^FAIL ' || true)
if [ "$rc" -ne 0 ]; then
  fail=$((fail + 1))
fi

printf '%s\n' "$result" | sed -e 's/^PASS /PASS  /' -e 's/^FAIL /FAIL  /'
echo
printf '合计：PASS=%d FAIL=%d\n' "$pass" "$fail"
if [ "$fail" -eq 0 ]; then echo '结论：文档站门禁通过'; else echo '结论：存在未通过项'; fi
exit "$fail"
