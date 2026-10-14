#!/usr/bin/env bash
# check_test_hygiene.sh — 单测卫生棘轮（守卫测试本身，不守卫生产代码）
#
# 背景：2026-10-14 整理全仓单测时清出三类"看起来在测、实际永远不会失败"的用例：
#   - 只有 t.Logf 没有断言的"可视化 dump"（internal/tui 一批）；
#   - 函数体第一层就是裸 t.Skip 的空壳用例（user_confirm_test.go 的 OnUserResponse ——
#     整个函数体只有一行 skip，它永远计入 PASS）；
#   - 恒真断言（只发布 1000 条却断言 count>1000；20 条相同 ref 让去重先把数量吃掉）。
# 它们全部计入 PASS，于是套件给了假的安全感 —— 这就是本脚本要堵的口子。
#
# 三条规则：
#   1. 每个 Test* 函数必须能失败：自身含 t.Error*/t.Fatal*/t.Fail*，
#      或调用按约定命名的断言辅助函数（assert*/check*/verify*/expect*/require*），
#      或写明豁免标记 hygiene:panic-guard（唯一失败模式是 panic 的 fail-safe 守卫用例）。
#      TestMain 是脚手架，不参与。
#   2. 函数体第一层（缩进 <=1 个制表符）不得出现裸 t.Skip*：
#      按环境跳过必须写成带条件的形式（if cond { t.Skip("原因") }）。
#   3. 同一个包里只允许一个 ANSI 剥离辅助函数（历史上四份 copies 各自漂移过）。
#
# 函数边界用"字符串感知的花括号深度"判定：夹具里的原始字符串常含顶格的 } 甚至
# func main() {，靠行首匹配或裸数括号都会把函数提前截断（曾经产生假阳性）。
#
# 这是一把棘轮：PENDING 里是已知待修项，只减不增；往里加条目必须评审。
#
# 用法：bash scripts/check_test_hygiene.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# 已知待修（只减不增；格式 <file>:<TestName>）
PENDING=(
	# 已清空。
)

TEST_FILES=$(find . -path ./codeseek -prune -o -name '*_test.go' -print | sort)
if [[ -z "$TEST_FILES" ]]; then
	echo "ERROR: 没找到任何 *_test.go" >&2
	exit 1
fi

AWK_PROG='
function report(kind, line) { printf "%s|%s:%s|%s\n", kind, fpath, line, fname }
function is_test() { return fname ~ /^Test[A-Z0-9]/ && fname != "TestMain" }

function eval_block(    hard, deleg) {
	seen++
	if (!is_test()) { fname = ""; return }
	hard  = (body ~ /t\.(Error|Errorf|Fatal|Fatalf|Fail|FailNow)\(/)
	deleg = (body ~ /(assert|check|verify|expect|require)[A-Z][A-Za-z0-9_]*\(/)
	# 显式豁免：fail-safe 守卫类用例的唯一失败模式就是 panic（去掉 nil 检查即 panic → 测试失败）。
	# 标记 hygiene:panic-guard 必须由作者写明，只有这一种豁免途径，避免规则被静默绕开。
	guard = (body ~ /hygiene:panic-guard/)
	if (!hard && !deleg && !guard) report("NOASSERT", fstart)
	fname = ""; body = ""; depth = 0; inq = ""; blockcom = 0
}

# 每个文件从干净状态开始：定界一旦失步，绝不能污染后续文件
# （否则 fname 一直非空，整个剩余代码库都被当成"函数体"跳过 —— 守卫自己静默失效）。
FNR == 1 { fname = ""; body = ""; depth = 0; inq = ""; blockcom = 0 }

# 单遍字符扫描：字符串/注释/原始串内的花括号不计入定界。
# （先剥 // 注释再扫会吃掉夹具里的 https:// 与其闭合反引号，定界随即失步。）
function scan_line(s,    i, n, c, d0) {
	d0 = depth
	n = length(s)
	for (i = 1; i <= n; i++) {
		c = substr(s, i, 1)
		if (inq == "" && !blockcom) {
			if (substr(s, i, 2) == "//") return d0      # 行注释到行尾
			if (substr(s, i, 2) == "/*") { blockcom = 1; i++; continue }
			if (c == "\"") { inq = c; continue }
			if (c == "`") { inq = c; continue }
			if (c == "{") depth++
			else if (c == "}") { depth--; if (depth == 0) return -1 }
			continue
		}
		if (blockcom) {
			if (substr(s, i, 2) == "*/") { blockcom = 0; i++ }
			continue
		}
		if (inq == "\"" && c == "\\") { i++; continue }
		if (c == inq) inq = ""
	}
	if (depth == 0 && d0 > 0 && inq == "") return -1
	return d0
}

{
	if (fname == "") {
		# 不在函数内：只认顶层 func 声明（inq 必须为空，避免被上一块的失步影响）
		if ($0 ~ /^func [A-Za-z_][A-Za-z0-9_]*\(/) {
			fname = substr($0, 6); sub(/\(.*/, "", fname)
			fpath = FILENAME; fstart = FNR; body = ""; depth = 1; inq = ""; blockcom = 0
		}
		next
	}

	if ($0 !~ /^[ \t]*$/) body = body $0 "\n"

	if (is_test() && $0 ~ /t\.Skip(f|a)?\(/) {
		ind = $0; sub(/[^ \t].*/, "", ind)
		if (ind == "" || ind == "\t") report("SKIP", FNR)
	}

	if (scan_line($0) == -1) { eval_block(); next }
	if (inq == "" && substr($0, length($0)) != "{") {
		# gofmt 的顶层函数体一定以 { 结尾；此处不是说明定界已异常，直接收口避免无限扩张
		if (depth <= 0) eval_block()
	}
}
END { if (fname != "") eval_block(); printf "SCANNED|%s:%s|\n", seen, NR }
'

SCAN=$(mktemp)
trap 'rm -f "$SCAN"' EXIT
# shellcheck disable=SC2086
awk "$AWK_PROG" $TEST_FILES > "$SCAN" || true

fail=0

is_pending() {
	local key="$1"
	for p in ${PENDING[@]+"${PENDING[@]}"}; do
		[[ "$p" == "$key" ]] && return 0
	done
	return 1
}

scanned=0
scanned_lines=0
while IFS='|' read -r kind loc name; do
	[[ -z "${kind:-}" ]] && continue
	if [[ "$kind" == "SCANNED" ]]; then
		scanned="${loc%%:*}"
		scanned_lines="${loc##*:}"
		continue
	fi
	if is_pending "$loc:$name"; then echo "PENDING: $loc:$name"; continue; fi
	case "$kind" in
		NOASSERT)
			echo "ERROR: 测试永远不会失败（无 t.Error*/t.Fatal*，也没调用 assert*/check*/verify* 辅助）：$loc:$name" >&2
			fail=1 ;;
		SKIP)
			echo "ERROR: 函数体第一层的裸 t.Skip —— 用例永久空转却计入 PASS：$loc:$name" >&2
			fail=1 ;;
	esac
done < "$SCAN"

# 守卫自己也不能静默失效：扫到的顶层函数与测试文件数量对不上就是脚本坏了
file_count=$(printf '%s\n' "$TEST_FILES" | grep -c .)
if [[ "$scanned" -lt 50 || "$scanned_lines" -lt 1 ]]; then
	echo "ERROR: 卫生检查自身异常（只扫到 ${scanned} 个顶层函数 / ${scanned_lines} 行，测试文件有 ${file_count} 个）—— 定界逻辑失步，不要相信本次结果" >&2
	exit 1
fi
echo "已扫描 ${scanned} 个顶层函数（${file_count} 个 *_test.go）"

# ─── 规则 3：每个包一个 ANSI 剥离器 ──────────────────────────────────────────
# 只统计 string→string 的非导出剥离器（^func name(s string) string）；
# Test 函数名里带 StripANSI、以及 stripANSILines 这类委托型伴生函数不算重复实现。
PKG_DIRS=$(sed 's|/[^/]*$||' <<< "$TEST_FILES" | sort -u)
for dir in $PKG_DIRS; do
	[[ -d "$dir" ]] || continue
	matches=$(grep -hE '^func [a-z][A-Za-z0-9_]*\([a-z] string\) string \{' "$dir"/*_test.go 2>/dev/null \
		| grep -iE 'strip|ansi' || true)
	defs=$(printf '%s' "$matches" | grep -c . || true)
	if [[ "${defs:-0}" -gt 1 ]]; then
		echo "ERROR: $dir 里定义了 $defs 个 ANSI 剥离辅助函数（请合并为一个）：" >&2
		grep -nE '^func [a-z][A-Za-z0-9_]*\([a-z] string\) string \{' "$dir"/*_test.go 2>/dev/null \
			| grep -iE 'strip|ansi' | sed 's/^/  /' >&2
		fail=1
	fi
done

if [[ $fail -ne 0 ]]; then
	echo "" >&2
	echo "改法：断言-less 的用例要么补上真实断言（钉住它名字声称的那件事），要么删除；" >&2
	echo "     按环境跳过必须写成带条件的形式（if cond { t.Skip(\"原因\") }）。" >&2
	exit 1
fi

echo "OK: 测试卫生棘轮未退（无断言-less 用例 / 无第一层裸 skip / 每包一个 ANSI 剥离器）"
exit 0
