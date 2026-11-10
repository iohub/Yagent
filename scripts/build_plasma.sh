#!/usr/bin/env bash
# build_plasma.sh — 把 plasma/ 打成单文件 zipapp（ADR-8）。
#
# 为什么不用 `python3 -m zipapp`：它用源文件的 mtime 做 ZIP 时间戳，于是每次
# 重新 checkout / touch 都换一次字节，`check_plasma_pyz_sync.sh` 这种"生成物必须
# 与源同步"的闸门就永远红。这里用内联 Python 自己写 ZIP：文件按名字排序、
# 时间戳固定 1980-01-01、STORED（不压缩，跨 zlib 版本也逐字节相同）。
#
# 用法：bash scripts/build_plasma.sh [输出路径]
#   缺省输出 dist/bin/plasma.pyz（go:embed dist/bin/* 会把它嵌进二进制）。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="${1:-$ROOT/dist/bin/plasma.pyz}"

if ! command -v python3 >/dev/null 2>&1; then
	echo "ERROR: 需要 python3（>=3.9）才能构建 plasma.pyz" >&2
	exit 1
fi

mkdir -p "$(dirname "$OUT")"
python3 - "$ROOT" "$OUT" <<'PY'
import os
import sys
import zipfile

root, out = sys.argv[1], sys.argv[2]
src = os.path.join(root, "plasma")
if not os.path.isdir(src):
    sys.exit("ERROR: 找不到 %s" % src)

# 只打进运行期需要的文件：*.py 与 prompts/*.md。
# tests/testdata 不打进去（它们不是运行期依赖，打进去只是把 pyz 变大）。
members = []
for dirpath, dirnames, filenames in os.walk(src):
    dirnames[:] = sorted(d for d in dirnames if d not in ("tests", "testdata", "__pycache__"))
    for name in sorted(filenames):
        if not (name.endswith(".py") or name.endswith(".md")):
            continue
        full = os.path.join(dirpath, name)
        rel = os.path.relpath(full, src).replace(os.sep, "/")
        members.append((rel, full))
members.sort()

if not any(rel == "__main__.py" for rel, _ in members):
    sys.exit("ERROR: plasma/__main__.py 缺失，zipapp 无法启动")

SHEBANG = b"#!/usr/bin/env python3\n"
FIXED_TIME = (1980, 1, 1, 0, 0, 0)
FILE_MODE = 0o644 << 16

tmp = out + ".tmp"
with open(tmp, "wb") as fh:
    fh.write(SHEBANG)
    with zipfile.ZipFile(fh, "w", compression=zipfile.ZIP_STORED) as zf:
        for rel, full in members:
            info = zipfile.ZipInfo(rel, date_time=FIXED_TIME)
            info.compress_type = zipfile.ZIP_STORED
            info.create_system = 3
            info.external_attr = FILE_MODE
            with open(full, "rb") as srcf:
                zf.writestr(info, srcf.read())
os.replace(tmp, out)
print("wrote %s (%d files, %d bytes)" % (out, len(members), os.path.getsize(out)))
PY

chmod 0700 "$OUT"
