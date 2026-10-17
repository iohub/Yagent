#!/usr/bin/env bash
# check_blueprint_leaf.sh — 蓝图包的 leaf 守卫
#
# 背景：internal/blueprint 的文档会被三类消费者读：agents（落盘钩子与 read_blueprint）、
# agents/director（每轮索引注入）、app（装配与启动清理）。它一旦反向 import 任何一边，
# 依赖就成环，而 `check_no_agents_import.sh` 那条"director 子包不得 import agents"的
# 守卫也会连带失效。同时启用判据也不许长在包里 —— 唯一判据是 config.BlueprintEnabled()
# （设计稿 §8 的 I8），配置键与环境变量各写一半判据就是这个仓库反复清理的那类病。
#
# 允许的依赖：Go 标准库 + yagent/internal/util（原子写内核）+ yagent/internal/projectid
# （与 rollout/artifact 共用同一把主键，见设计稿 §11）。
#
# 用法：bash scripts/check_blueprint_leaf.sh
# 退出码：0 = 通过；非 0 = 发现越界依赖。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

SELF="yagent/internal/blueprint"
ALLOWED="^yagent/internal/(util|projectid)$"

deps="$(go list -deps ./internal/blueprint)"

offenders="$(echo "$deps" | grep '^yagent/' | grep -vx "$SELF" | grep -Ev "$ALLOWED" || true)"

if [[ -n "$offenders" ]]; then
	echo "ERROR: $SELF 不再是 leaf 包，它依赖了仓库内的其他包：" >&2
	echo "$offenders" | sed 's/^/  /' >&2
	echo "" >&2
	echo "Fix: 需要的外部能力一律由调用方作为参数传进来（projectID、启用状态、预算都是值），" >&2
	echo "     不要往 blueprint 里 import agents / tools / blackboard / config。" >&2
	exit 1
fi

echo "OK: $SELF 只依赖标准库 + util + projectid"
exit 0
