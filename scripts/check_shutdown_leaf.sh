#!/usr/bin/env bash
# check_shutdown_leaf.sh — internal/shutdown 的 leaf 守卫（只准标准库 + supervise）
#
# 背景：这个包是"信号 → ctx 取消"的唯一落点。它一旦反向依赖任何业务包（tools、agents、app），
# 就同时违反两条纪律：① 依赖图成环（那些包都要用 ctx 取消链，而取消链在它们之上）；
# ② "第二次信号硬退出前要杀掉我们起的进程组"这件事会从"调用方传进来的钩子"退化成
# 本包自己去 import tools —— 而这条钩子必须留在 main.go 里，因为它决定杀的顺序。
#
# 允许的依赖：Go 标准库 + yagent/internal/supervise（本包那条看门狗 goroutine 的落脚点，
# 全仓 goroutine 只能经 supervise 起，见 scripts/check_no_bare_go.sh）。
#
# 用法：bash scripts/check_shutdown_leaf.sh
# 退出码：0 = 通过；非 0 = 发现业务依赖。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

SELF="yagent/internal/shutdown"
ALLOWED="yagent/internal/supervise"

deps="$(go list -deps ./internal/shutdown)"

offenders="$(echo "$deps" | grep '^yagent/' | grep -vx -e "$SELF" -e "$ALLOWED" || true)"

if [[ -n "$offenders" ]]; then
	echo "ERROR: $SELF 不再是 leaf 包，它依赖了仓库内的其他包：" >&2
	echo "$offenders" | sed 's/^/  /' >&2
	echo "" >&2
	echo "Fix: 信号到了之后做什么（通知操作者、杀进程组、退出码）一律走 Config 的钩子，" >&2
	echo "     由 main.go 装配；本包只把信号翻译成一次 ctx 取消。" >&2
	exit 1
fi

echo "OK: $SELF 只依赖标准库 + supervise"
exit 0
