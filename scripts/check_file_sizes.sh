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
# 棘轮：存量超限项在 PENDING 里点名冻结 —— 每项记录"当前行数上限"，
# 只许减不许增。这样守卫对新增改动永远是硬的，而历史债务不会被顺手藏起来
# （以前它直接红灯，于是所有人对它视而不见）。拆到阈值以下就把该项删掉。
#
# 用法：在项目根目录执行 `bash scripts/check_file_sizes.sh`
# 退出码：0 = 通过；非 0 = 出现新的超限或存量项膨胀。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

TARGET_DIR="internal/agents/director"
MAX_LINES=300

# 格式 <相对路径>:<允许的最大行数>
PENDING=(
	# planner_run.go：主循环 + 压缩 + 委派编排三段职责，拆分是独立一次改动。
	# P1 加暂停闸门要进 9 行（循环顶部 8 行 + pause import），换来的是把 runState
	# 闭包挪进 planner_rollout.go（省 8 行）和把一条三行 slog 收成一行（省 2 行），
	# 所以行数不升反降 —— 上限随之下调。
	# 压缩段（96 行的三档判定与动作）已抽到 planner_compress.go，657 → 572；
	# assistant/tool 两轮消息改走 llm.Choice 的单一构造点，572 → 557。
	# 本轮用户输入的落库抽到 planner_memory.go（FirstConversation 必须在写入之前取快照，
	# 主循环里只留快照与注释），557 → 556。
	# D2（终止收口）要给主循环加一条兜底 defer、D1 要给 exec 闭包加 recover，两者合计 +10 行
	# 会顶破冻结上限：于是把 39 行的 writeDirectorRollout 闭包搬进 planner_rollout.go
	# （它就是"记录"，与终止事件同属一个文件），journal 构造点顺带承载兜底 defer，556 → 532。
	# 剩下主循环 + 委派编排两段职责，仍是一次独立改动。
	"internal/agents/director/planner_run.go:532"
)

if [ ! -d "$TARGET_DIR" ]; then
    echo "ERROR: directory not found: $TARGET_DIR" >&2
    exit 1
fi

pending_ceiling() {
	local want="$1"
	local entry
	for entry in "${PENDING[@]}"; do
		if [[ "${entry%%:*}" == "$want" ]]; then
			echo "${entry##*:}"
			return 0
		fi
	done
	return 1
}

violations=()
stale=()
while IFS= read -r file; do
	lines="$(awk 'END{print NR}' "$file")"
	rel="${file#$ROOT/}"
	if ceiling="$(pending_ceiling "$rel")"; then
		# 存量项：只许减；长回去照样拦。
		if [ "$lines" -gt "$ceiling" ]; then
			violations+=("${rel}: ${lines} lines (冻结上限 ${ceiling}，只能降不能升)")
		fi
	elif [ "$lines" -gt "$MAX_LINES" ]; then
		violations+=("${rel}: ${lines} lines (limit ${MAX_LINES})")
	fi
done < <(find "$TARGET_DIR" -name '*.go' ! -name '*_test.go')

# PENDING 里指向已消失/已达标的文件也要报：否则棘轮名单会变成假债务。
for entry in "${PENDING[@]}"; do
	rel="${entry%%:*}"
	if [ ! -f "$rel" ]; then
		stale+=("${rel} 已不存在，请从 PENDING 删除该项")
		continue
	fi
	ceiling="${entry##*:}"
	actual="$(awk 'END{print NR}' "$rel")"
	if [ "$actual" -le "$MAX_LINES" ]; then
		stale+=("${rel}: ${actual} 行已达标，请从 PENDING 删除（棘轮收紧）")
	elif [ "$actual" -lt "$ceiling" ]; then
		stale+=("${rel}: 现在 ${actual} 行，请把冻结上限从 ${ceiling} 下调到 ${actual}")
	fi
done

if [ ${#violations[@]} -gt 0 ]; then
    echo "ERROR: non-test .go files in ${TARGET_DIR} exceed the line budget:" >&2
    printf '  %s\n' "${violations[@]}" >&2
    exit 1
fi

if [ ${#stale[@]} -gt 0 ]; then
    echo "ERROR: check_file_sizes 的 PENDING 名单与实际不同步（棘轮必须收紧）:" >&2
    printf '  %s\n' "${stale[@]}" >&2
    exit 1
fi

echo "OK: ${TARGET_DIR} 组件层行数守卫通过（PENDING 冻结 ${#PENDING[@]} 项）"
