#!/usr/bin/env bash
# check_supervise_leaf.sh — internal/supervise 的 leaf 守卫（只准标准库）
#
# 背景：supervise 是全仓 goroutine 的落脚点 —— 任务循环、工具批、MCP 读循环、后台 worker
# 都要 import 它。它一旦反向依赖任何一个业务包，依赖图立刻成环（工具批在 agents 里，
# 而 agents 要用 supervise；bus 在 messaging 里，而 bus 也要用 supervise），
# 而"谁能 import 谁"这种约束只有守卫能守住 —— 注释挡不住一次顺手加依赖。
#
# 允许的依赖：只有 Go 标准库。域的名字、ctx、超时全部由调用方传进来；
# 需要日志/指标就在 Drain()/Failures() 的返回值上做，不要把 slog 以外的东西拉进来。
#
# 用法：bash scripts/check_supervise_leaf.sh
# 退出码：0 = 通过；非 0 = 发现仓库内依赖。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

SELF="yagent/internal/supervise"

deps="$(go list -deps ./internal/supervise)"

offenders="$(echo "$deps" | grep '^yagent/' | grep -vx "$SELF" || true)"

if [[ -n "$offenders" ]]; then
	echo "ERROR: $SELF 不再是 leaf 包，它依赖了仓库内的其他包：" >&2
	echo "$offenders" | sed 's/^/  /' >&2
	echo "" >&2
	echo "Fix: supervise 只能被依赖，不能依赖别人。要传进来的东西（名字、ctx、超时、" >&2
	echo "     退出后的动作）一律做参数或返回值。" >&2
	exit 1
fi

echo "OK: $SELF 只依赖标准库"
exit 0
