#!/usr/bin/env bash
# check_file_sizes.sh — director/ 子包组件文件行数守卫（P0-1 Phase 0 资产）
#
# 背景：P0-1 将把 1846 行的 internal/agents/director.go 拆分为
# internal/agents/director/ 子包组件。本脚本守卫组件层单文件不超过 300 行，
# 防止拆分过程中组件重新膨胀。
#
# 注意：只检查组件层 internal/agents/director/ 目录，
# 不检查门面层 internal/agents/director.go（其达标是后续阶段的目标）。
#
# 用法：在项目根目录执行 `bash scripts/check_file_sizes.sh`
# 退出码：0 = 通过；非 0 = 存在超限文件。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

TARGET_DIR="internal/agents/director"
MAX_LINES=300

if [ ! -d "$TARGET_DIR" ]; then
    echo "ERROR: directory not found: $TARGET_DIR" >&2
    exit 1
fi

violations=()
while IFS= read -r file; do
    lines="$(wc -l < "$file")"
    if [ "$lines" -gt "$MAX_LINES" ]; then
        violations+=("${file}: ${lines} lines (limit ${MAX_LINES})")
    fi
done < <(find "$TARGET_DIR" -name '*.go' ! -name '*_test.go' -type f | sort)

if [ "${#violations[@]}" -gt 0 ]; then
    echo "ERROR: non-test .go files in ${TARGET_DIR} exceed ${MAX_LINES} lines:" >&2
    for v in "${violations[@]}"; do
        echo "  $v" >&2
    done
    exit 1
fi

echo "OK: all non-test .go files in ${TARGET_DIR} are within ${MAX_LINES} lines"
exit 0
