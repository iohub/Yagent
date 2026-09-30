#!/usr/bin/env bash
# check_no_agents_import.sh — 循环依赖守卫（P0-1 Phase 0 资产）
#
# 背景：internal/agents 包已 import internal/agents/director 子包，
# 因此 director 子包（及其全部传递依赖）绝不能反向 import agents，
# 否则将产生 Go 循环依赖，编译失败。本脚本永久守卫该硬约束。
#
# 用法：在项目根目录执行 `bash scripts/check_no_agents_import.sh`
# 退出码：0 = 通过；非 0 = 检测到违规依赖。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

TARGET_PKG="yagent/internal/agents"
DIRECTOR_PKGS="./internal/agents/director/..."

# 获取 director 子包及其全部传递依赖
deps="$(go list -deps "$DIRECTOR_PKGS")"

# 整行精确匹配（grep -x）：director 自身路径 yagent/internal/agents/director
# 包含 yagent/internal/agents 子串，子串匹配会误伤，必须用整行匹配。
if echo "$deps" | grep -qx "$TARGET_PKG"; then
    echo "ERROR: cyclic dependency detected — director subpackage dependency graph imports ${TARGET_PKG}" >&2
    echo "" >&2
    echo "Offending package(s) (importing ${TARGET_PKG}):" >&2
    for pkg in $deps; do
        if go list -f '{{join .Imports "\n"}}' "$pkg" 2>/dev/null | grep -qx "$TARGET_PKG"; then
            echo "  $pkg" >&2
        fi
    done
    echo "" >&2
    echo "Fix: remove the \"${TARGET_PKG}\" import from the offending package(s)." >&2
    exit 1
fi

echo "OK: no package in the director dependency graph imports ${TARGET_PKG}"
exit 0
