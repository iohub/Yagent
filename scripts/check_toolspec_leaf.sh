#!/usr/bin/env bash
# check_toolspec_leaf.sh — 工具注册表的 leaf 守卫
#
# 背景：internal/toolspec 持有 Spec/能力标签/画像/校验/声明文件，被 internal/tools、
# internal/agents、internal/compression、internal/tui 共同 import。一旦它反向 import
# 任何一个消费者，整套依赖就成环，注册表会被迫拆回多份 —— 那正是本次重构消灭的东西
# （docs/tool-registry-design.md）。
#
# 允许的依赖：Go 标准库 + yagent/internal/toolspec 自身。
# （实现一度允许 import internal/llm，后来改为由 Deps 传入已绑定的执行器，
#  于是连 llm 都不需要，守卫收到最严。）
#
# 用法：bash scripts/check_toolspec_leaf.sh
# 退出码：0 = 通过；非 0 = 发现越界依赖。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

SELF="yagent/internal/toolspec"
deps="$(go list -deps ./internal/toolspec)"

offenders="$(echo "$deps" | grep '^yagent/' | grep -vx "$SELF" || true)"

if [[ -n "$offenders" ]]; then
    echo "ERROR: $SELF 不再是 leaf 包，它依赖了仓库内的其他包：" >&2
    echo "$offenders" | sed 's/^/  /' >&2
    echo "" >&2
    echo "Fix: 把需要的外部能力改成 toolspec.Deps 的窄接口字段，由调用方在装配期注入；" >&2
    echo "     不要往 toolspec 里 import 实现包。" >&2
    exit 1
fi

echo "OK: $SELF 只依赖标准库"
exit 0
