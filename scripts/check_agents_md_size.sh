#!/usr/bin/env bash
# check_agents_md_size.sh — AGENTS.md 体积棘轮（docs/agents-md-slimming-design.md §6）
#
# 背景：仓库根 AGENTS.md 被 internal/agents/director/project_context.go 逐字、无上限地注入
# 每一次 Director 会话。它长到 84,484 字符（≈53k token）时，120k 档的压缩阈值有四成在第一条
# 用户消息之前就被吃掉——文档膨胀在这个仓库里不是整洁问题，是运行时行为问题。
# 2026-10-24 那轮把它压到 31k 字符，本脚本钉住"不许再长回去"。
#
# 棘轮（照 check_file_sizes.sh 的形状，不是硬门）：表里每项冻结"当前行数上限"，只许减不许增；
# 新文件超过 NEW_FILE_MAX_LINES 直接红；文件变小了自己报失步。
# 一条常红的守卫等于没有守卫。
#
# 用法：bash scripts/check_agents_md_size.sh
# 退出码：0 = 通过；非 0 = 有文件超限或名单与实际不同步。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# 没进名单的新文档的硬上限：低于当前最长的那几份，让"新增"这件事本身偏向下坡。
NEW_FILE_MAX_LINES=200

# 格式 <相对路径>:<允许的最大行数>
PENDING=(
	# 只有"已经超过 NEW_FILE_MAX_LINES 的存量"才需要进这张表；其余文档由硬上限直接管着，
	# 谁长过 200 行再决定是收缩还是显式立项（显式立项就是承认一笔要还的债）。
	#
	# 根文件：跨模块决策面 + 路由 + §11 的 45 条坑索引（每条一行）。行数偏大是形状决定的——
	# 45 行索引、§7 的 TOML 契约块、§6.7 的持久表都是"一行一件事"；真正的成本口径是 token
	# （当前约 17.7k，瘦身前约 53k）。要收就在小节内部删，**不要重排 §5.x/§6.x/§11 的编号**：
	# 17 份嵌套文档有 25 处指针指回它们。
	"AGENTS.md:608"
	# tui / agents 两份是 2026-10-24 从根文件下沉包内判据的结果（根不再复述同一件事）。
	"internal/tui/AGENTS.md:226"
	"internal/agents/AGENTS.md:212"
)

pending_ceiling() {
	local want="$1" entry
	for entry in "${PENDING[@]}"; do
		if [[ "${entry%%:*}" == "$want" ]]; then
			echo "${entry##*:}"
			return 0
		fi
	done
	return 1
}

# 不用 mapfile（macOS 自带 bash 3.2 没有它）。
files=()
while IFS= read -r f; do
	files+=("$f")
done < <(find . \
	-path ./.git -prune -o -path ./codeseek -prune -o -path '*/node_modules' -prune -o \
	-name 'AGENTS.md' -print | sed 's|^\./||' | sort)

if [ "${#files[@]}" -eq 0 ]; then
	echo "ERROR: 一个 AGENTS.md 都没扫到——目录结构变了，请同步本脚本" >&2
	exit 1
fi

# 名单覆盖性：新文档必须显式进名单或低于硬上限，两种都不做就是没被扫到。
covered=0
for f in "${files[@]}"; do
	for entry in "${PENDING[@]}"; do
		[[ "${entry%%:*}" == "$f" ]] && covered=$((covered + 1))
	done
done
if [ "$covered" -lt "${#PENDING[@]}" ]; then
	echo "ERROR: PENDING 名单里有 ${#PENDING[@]} 项，实际只匹配到 $covered 项（磁盘上 ${#files[@]} 份文档）" >&2
	exit 1
fi

violations=()
stale=()
for rel in "${files[@]}"; do
	lines="$(awk 'END{print NR}' "$rel")"
	if ceiling="$(pending_ceiling "$rel")"; then
		if [ "$lines" -gt "$ceiling" ]; then
			violations+=("${rel}: ${lines} 行，超过冻结上限 ${ceiling}（只许减；包内细节请写进对应包的 AGENTS.md 而不是根文件）")
		fi
	elif [ "$lines" -gt "$NEW_FILE_MAX_LINES" ]; then
		violations+=("${rel}: ${lines} 行，新文档上限 ${NEW_FILE_MAX_LINES}（该收缩或拆分内容，不要直接加进名单）")
	fi
done

for entry in "${PENDING[@]}"; do
	rel="${entry%%:*}"
	if [ ! -f "$rel" ]; then
		stale+=("${rel} 已不存在，请从 PENDING 删除该项")
		continue
	fi
	ceiling="${entry##*:}"
	actual="$(awk 'END{print NR}' "$rel")"
	if [ "$actual" -le "$NEW_FILE_MAX_LINES" ]; then
		stale+=("${rel}: ${actual} 行已达新文档上限，请从 PENDING 删除（棘轮收紧）")
	elif [ "$actual" -lt "$ceiling" ]; then
		stale+=("${rel}: 现在 ${actual} 行，请把冻结上限从 ${ceiling} 下调到 ${actual}")
	fi
done

if [ ${#violations[@]} -gt 0 ]; then
	echo "ERROR: AGENTS.md 体积超限:" >&2
	printf '  %s\n' "${violations[@]}" >&2
	exit 1
fi

if [ ${#stale[@]} -gt 0 ]; then
	echo "ERROR: check_agents_md_size 的 PENDING 名单与实际不同步（棘轮必须收紧）:" >&2
	printf '  %s\n' "${stale[@]}" >&2
	exit 1
fi

total=0
for rel in "${files[@]}"; do
	total=$((total + $(awk 'END{print NR}' "$rel")))
done
echo "OK: AGENTS.md 体积守卫通过（${#files[@]} 份文档，合计 ${total} 行，冻结 ${#PENDING[@]} 项）"
