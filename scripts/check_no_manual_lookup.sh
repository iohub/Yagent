#!/usr/bin/env bash
# check_no_manual_lookup.sh — 禁止手工工具 lookup 复活（棘轮式守卫）
#
# 背景：重构前每个 Agent 各写一个 `switch name { case "read_file": ... }`，再加
# 若干按工具名索引的元数据表；声明与实现靠字符串对齐，漏一个 case 就是工具静默消失
# （Coding 拿不到 thinking/query_call_graph、Custom Agent 拿不到 meta prompt 广告过的
#  semantic_search —— 详见 docs/tool-registration.md §8）。
# 现在装配统一走 tools.NewSurface(profile, deps)。
#
# 这是一把棘轮：FORBIDDEN 里的东西必须为零；PENDING 里是 step 6 尚未迁完的 Agent，
# 每迁完一个就从名单里删掉一行，禁止往里加新行。
#
# 用法：bash scripts/check_no_manual_lookup.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# 已经消灭的东西：不得再出现在任何非注释代码里。
FORBIDDEN=(
	getToolFunc
	buildToolAdapters
	buildDelegateAdapters
	buildChatAdapters
	buildDevOpsAdapters
	nonParallelToolNames
	dangerousTools
	pathParamNames
	toolTruncationPriority
	skipBodyTools
)

# step 6 未完：这些 Agent 仍在 legacy 装配路径上，允许存在但必须可见。
PENDING=(
	# 已清空：六个子 Agent 与 Director 全部走 tools.NewSurface。
	# 往这里加条目等于重新引入静默失效，必须评审。
)

fail=0

# 只匹配代码：剔除行注释与块注释描述（历史名字在文档性注释里出现是必要的）。
strip_comments() {
	sed -E 's://.*$::'
}

check_symbol() {
	local sym="$1"
	local hits
	hits="$(grep -rn --include='*.go' "\b$sym\b" internal/ 2>/dev/null \
		| grep -v '_test\.go:' \
		| strip_comments \
		| grep -E "\b$sym\b" || true)"
	[[ -z "$hits" ]] && return 0

	# PENDING 里的符号允许出现在它自己的 legacy 路径与注册表注释处
	local allowed_for_pending=0
	for p in "${PENDING[@]}"; do
		[[ "$p" == "$sym" ]] && allowed_for_pending=1
	done
	if [[ $allowed_for_pending -eq 1 ]]; then
		echo "PENDING (step 6 未完): $sym 仍在以下位置："
		echo "$hits" | sed 's/^/  /'
		return 0
	fi

	echo "ERROR: 被禁止的手工 lookup / 名字表又出现了：$sym" >&2
	echo "$hits" | sed 's/^/  /' >&2
	return 1
}

for sym in "${FORBIDDEN[@]}"; do
	check_symbol "$sym" || fail=1
done
for sym in ${PENDING[@]+"${PENDING[@]}"}; do
	check_symbol "$sym" || fail=1
done

# Agent 侧不得再出现"按工具名的 case 分派"（测试与 TUI 的参数格式化不在禁止范围）。
bad_cases="$(grep -rn --include='*.go' -E 'case "(read_file|create_file|delete_file|rename_file|search_replace_in_file|run_bash|list_dir|print_dir_tree|search_by_regex|semantic_search|query_code_[a-z]+|find_function_[a-z]+|query_call_graph|deepthinking|micro_agent|thinking|design|agent_exit|ask_user_for_help|consolidate_knowledge|prune_history|read_note|read_artifact|load_tool|git_checkpoint_[a-z]+|browser_[a-z]+|delegate_[a-z_]+)"' internal/agents \
	| strip_comments | grep -v '_test\.go' || true)"
if [[ -n "$bad_cases" ]]; then
	# step 6 未完的 Agent 文件出现在这里属于已知项；其余一律视为违规。
	known_pending_files='__none__\.go:'
	unknown_cases="$(echo "$bad_cases" | grep -Ev "$known_pending_files" || true)"
	pending_cases="$(echo "$bad_cases" | grep -E "$known_pending_files" || true)"
	if [[ -n "$pending_cases" ]]; then
		echo "PENDING (step 6 未完): 以下 Agent 仍按工具名 switch："
		echo "$pending_cases" | sed 's/^/  /' | sed 's/:.*case /: case /'
	fi
	if [[ -n "$unknown_cases" ]]; then
		echo "ERROR: internal/agents 里出现新的按工具名 case 分派：" >&2
		echo "$unknown_cases" | sed 's/^/  /' >&2
		fail=1
	fi
fi

if [[ $fail -ne 0 ]]; then
	echo "" >&2
	echo "改法：在实现旁边 toolspec.MustRegister(Spec{...})，能力/频率档/授权要求写在 Spec 上，" >&2
	echo "     由 tools.NewSurface(profile, deps) 装配（docs/tool-registry-design.md）。" >&2
	exit 1
fi

echo "OK: 棘轮未退（FORBIDDEN 全零；PENDING 见上，迁完请同步删除）"
exit 0
