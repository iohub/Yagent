#!/usr/bin/env bash
# check_plasma_pyz_sync.sh — plasma.pyz 是生成物：必须与 plasma/ 源码逐字节同步。
#
# 与 schemas.json / golden 同一取向：生成物提交进仓库是为了 `go build ./...` 不依赖
# "先跑一遍脚本"，代价就是它会漂。漂的表现不是崩溃，而是**生产跑的是旧策略**：
# 改了 generate.py 却没重建 ⇒ 单测绿（测的是源码）、真机行为没变。
#
# 缺 python3 时**跳过并说明**（不是验证通过）：ADR-15 把"没有解释器"定义为正常的
# 能力降级，此时无法重建，也就无法判定同步。打印 WARN 让读的人知道这条闸门没生效。
#
# 用法：bash scripts/check_plasma_pyz_sync.sh
# 退出码：0 = 同步（或跳过）；非 0 = 漂移 / 缺产物。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

TARGET="dist/bin/plasma.pyz"

if ! command -v python3 >/dev/null 2>&1; then
	echo "WARN: PATH 上没有 python3 —— 跳过 plasma.pyz 同步校验（这不是'验证通过'）"
	exit 0
fi

if [[ ! -f "$TARGET" ]]; then
	echo "ERROR: 缺少 $TARGET —— 内嵌产物不存在，生产会降级成'plasma 未装配'。" >&2
	echo "修复：bash scripts/build_plasma.sh" >&2
	exit 1
fi

TMPDIR_BUILD="$(mktemp -d)"
trap 'rm -rf "$TMPDIR_BUILD"' EXIT
TMP_PYZ="$TMPDIR_BUILD/plasma.pyz"

bash scripts/build_plasma.sh "$TMP_PYZ" >/dev/null

if ! cmp -s "$TMP_PYZ" "$TARGET"; then
	echo "ERROR: $TARGET 与 plasma/ 源码不同步（生成物漂了）。" >&2
	echo "  期望 sha256: $(python3 -c 'import hashlib,sys;print(hashlib.sha256(open(sys.argv[1],"rb").read()).hexdigest())' "$TMP_PYZ")" >&2
	echo "  仓库 sha256: $(python3 -c 'import hashlib,sys;print(hashlib.sha256(open(sys.argv[1],"rb").read()).hexdigest())' "$TARGET")" >&2
	echo "修复：bash scripts/build_plasma.sh（并把 dist/bin/plasma.pyz 一起提交）" >&2
	exit 1
fi

echo "OK: $TARGET 与 plasma/ 源码逐字节一致（sha256=$(python3 -c 'import hashlib,sys;print(hashlib.sha256(open(sys.argv[1],"rb").read()).hexdigest())' "$TARGET")）"
