#!/usr/bin/env bash
# check_plasma_leaf.sh — Plasma 编译器包的 leaf 守卫
#
# 背景：internal/plasma 判定"一份生成出来的 bundle 能不能被信任地装配成 Agent"。
# 它的消费者是 agents（编译 + 加载 + trampoline 驱动）与 app（装配）。它一旦 import
# 任何一边，依赖就成环；而阶段 5 需要 RunAgentLoop、装配需要 Surface，这两件事都必须
# 由调用方经窄接口注入（FixtureRunner / Spawner），不是把包拉进来。
# 同时它是**裁决者**：裁决者认识被裁决者的执行细节，就没有独立判据可言了。
#
# 允许的依赖：Go 标准库 + toolspec（能力位数学与注册表查询，判据的单一来源）
# + util（原子写内核 / SnakeCase）+ projectid（与 rollout/artifact 共用主键）
# + tokenutil（阶段 4 的预算必须与压缩阈值同一个计数器）。
#
# 注意：go list -deps 不含测试文件的 import，所以 plasma_test 引入 agents/tools
# 来 populate 真实注册表（坑 39：测试必须与生产同形）不会打穿本守卫。
#
# 用法：bash scripts/check_plasma_leaf.sh
# 退出码：0 = 通过；非 0 = 发现越界依赖。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

SELF="yagent/internal/plasma"
ALLOWED="^yagent/internal/(toolspec|util|projectid|tokenutil)$"

deps="$(go list -deps ./internal/plasma)"

offenders="$(echo "$deps" | grep '^yagent/' | grep -vx "$SELF" | grep -Ev "$ALLOWED" || true)"

if [[ -n "$offenders" ]]; then
	echo "ERROR: $SELF 不再是 leaf 包，它依赖了仓库内的其他包：" >&2
	echo "$offenders" | sed 's/^/  /' >&2
	echo "" >&2
	echo "Fix: 执行能力一律由调用方注入 —— 夹具执行走 FixtureRunner、子进程走 Spawner、" >&2
	echo "     工具面装配留在 agents 侧（plasma 只产出 toolspec.Profile）。" >&2
	echo "     不要往 plasma 里 import agents / tools / llm / config / messaging。" >&2
	exit 1
fi

echo "OK: $SELF 只依赖标准库 + toolspec + util + projectid + tokenutil"
exit 0
