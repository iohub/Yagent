#!/usr/bin/env bash
# check_no_inline_atomic_write.sh — 原子写的单一实现守卫（设计稿 v4 的 I21）
#
# 背景：本仓曾经有 **6 份** "先写临时文件再 rename" 的实现
# （blackboard/artifact/datamanager/config-persist/config-template/memory-shared）。
# 同形实现的数量与漂移概率成正比：它们在权限位、失败清理、tmp 命名上各不相同，
# 而"读者永远不会看到半份文件"这件事恰恰是靠这些细节成立的。
# R1 把它们收进 internal/util/atomic.go，本脚本保证不再长出第二份。
#
# 判定的形状：同一个**非测试**文件里同时出现
#   A. os.Rename(            —— 改名落盘
#   B. os.WriteFile(.../os.CreateTemp(... 且目标名以 .tmp 结尾 —— 临时文件写入
# 只有两者都在，才构成"又一次手写的原子写"。
# internal/tools/file_operations.go 用 os.Rename 实现 rename_file 工具，
# 它的 ".tmp" 出现在安全路径名单里而不是写文件调用里，因此不会被误伤。
#
# 用法：bash scripts/check_no_inline_atomic_write.sh
# 退出码：0 = 通过；非 0 = 发现第二份实现。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

KERNEL="internal/util/atomic.go"

# 允许的形状：内核自己 + 各落盘点的转调（不含 rename/临时写）。
# 用 find 而不是 git ls-files：未提交的新文件同样要在扫描范围内，
# 否则"刚写出的第二份实现"要到 commit 之后才会被抓到。
files="$(find internal -name '*.go' ! -name '*_test.go' ! -path "internal/util/atomic.go" | sort)"
[[ -n "$files" ]] || { echo "ERROR: 一个 .go 文件都没扫到 —— 守卫自己坏了，不算通过" >&2; exit 1; }

offenders=""
scanned=0
for f in $files; do
	[[ -f "$f" ]] || continue
	scanned=$((scanned + 1))
	if grep -q 'os\.Rename(' "$f" &&
		grep -Eq 'os\.(WriteFile|CreateTemp)\([^)]*\.tmp' "$f"; then
		offenders="${offenders}${f}"$'\n'
	fi
done

if [[ -n "$offenders" ]]; then
	echo "ERROR: 发现 tmp+rename 的第二份实现（原子写只准有 internal/util/atomic.go 一处）：" >&2
	printf '%s' "$offenders" | sed 's/^/  /' >&2
	echo "" >&2
	echo "Fix: 改成 util.AtomicWrite(path, data, util.AtomicWriteOptions{...})，" >&2
	echo "     目录/文件权限与 tmp 后缀作为选项传入；不要就地再写一遍。" >&2
	exit 1
fi

echo "OK: 原子写实现唯一（扫描 ${scanned} 个非测试文件 + 内核 ${KERNEL}）"
exit 0
