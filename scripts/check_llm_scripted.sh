#!/usr/bin/env bash
# check_llm_scripted.sh — internal/llm/scripted 的 import 边界守卫
#
# 背景：internal/llm/scripted 是 5a 结构夹具在**生产路径**上的确定性替身（ADR-12）。
# 它一旦 import 配置层 / 工具层 / 装配层，两件事同时发生：依赖成环（agents 用它，
# 它反过来又要 agents 的实现），以及替身**开始依赖被测组件**——它就不再是判据独立的
# 那一份实现，接线错误会被两边同样的方式掩盖掉（坑 33 的变体）。身份解析、能力裁决、
# prompt 渲染一律由调用方注入：夹具身份走 NewForIdentity，e2e 走默认嗅探。
#
# 允许的依赖：Go 标准库 + yagent/internal/llm（它要实现 llm.Engine 接口，这是唯一
# 必需的仓库内依赖）。其余仓库内包一律禁止，尤其 config / tools / agents / mcp /
# messaging / toolspec。
#
# 注意：这里**刻意不用 go list -deps**（与 check_plasma_leaf.sh 不同）。plasma 的叶子性
# 问的是"整个传递闭包干净"，而 internal/llm 自己依赖 config/logging/models/util，
# -deps 会把 llm 的整条闭包误报成越界。本闸门的判据是本包**直接 import**，含测试文件的
# import（TestImports 为白盒测试、XTestImports 为外部测试包），两者都是"这个包真会拉
# 进来的东西"。
#
# 用法：bash scripts/check_llm_scripted.sh
# 退出码：0 = 通过；非 0 = 发现越界依赖。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

SELF="yagent/internal/llm/scripted"
ALLOWED='^yagent/internal/llm$'

# 三份 import 列表拼在一起：直接依赖 + 白盒测试 + 外部测试包。
imports="$(go list -f '{{join .Imports "\n"}}{{"\n"}}{{join .TestImports "\n"}}{{"\n"}}{{join .XTestImports "\n"}}' ./internal/llm/scripted)"

offenders="$(printf '%s\n' "$imports" | grep '^yagent/' | grep -vx "$SELF" | grep -Ev "$ALLOWED" || true)"

if [[ -n "$offenders" ]]; then
	echo "ERROR: $SELF 只能 import 标准库 + yagent/internal/llm，它却依赖了：" >&2
	printf '%s\n' "$offenders" | sed 's/^/  /' >&2
	echo "" >&2
	echo "Fix: 脚本引擎是**生产路径上的确定性替身**（5a 结构夹具靠它判断接线对不对）。" >&2
	echo "     一旦 import 配置/工具/装配层，依赖就成环，且替身开始依赖被测组件——" >&2
	echo "     接线错的两边会用同一种方式错，谁也不再是独立判据。" >&2
	echo "     身份解析、能力裁决、prompt 渲染由调用方注入（NewForIdentity / NewWithResolver）。" >&2
	exit 1
fi

echo "OK: $SELF 只依赖标准库 + yagent/internal/llm"
exit 0
