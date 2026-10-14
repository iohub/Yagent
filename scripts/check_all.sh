#!/usr/bin/env bash
# check_all.sh — 本地质量闸门：一条命令跑完"改代码前后都该跑的东西"。
#
# 背景：本仓没有 CI 质量闸门（.github/workflows/release.yml 只做跨平台构建与发布），
# 所以"跑没跑测试"完全靠人记。这个脚本把 build / vet / 四把守卫 / 全量单测串起来，
# 顺序按"失败最快、信息最便宜"排：先编译，再静态检查，再棘轮守卫，最后单测。
#
# 用法：
#   bash scripts/check_all.sh              # 全量
#   bash scripts/check_all.sh --fast       # 只跑受改动影响的包（跳过 ./... 全量单测）
#   RACE=true bash scripts/check_all.sh    # 额外用 -race 跑并发相关包（默认不跑，慢 3-8 倍）
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

FAST=false
for arg in "$@"; do
	case "$arg" in
	--fast) FAST=true ;;
	*) echo "用法: bash scripts/check_all.sh [--fast]" >&2; exit 2 ;;
	esac
done

step() { printf '\n\033[1;36m==> %s\033[0m\n' "$1"; }
fail() { printf '\033[1;31mFAIL: %s\033[0m\n' "$1" >&2; exit 1; }

step "go build ./..."
go build ./... || fail "编译失败"

step "go vet ./..."
go vet ./... || fail "go vet 不干净"

# 四把守卫：文件行数、包依赖方向、注册表叶子性、单测卫生。
# 每一个都对应一次"曾经静默漂移"的事故，别绕过。
for guard in check_no_manual_lookup check_toolspec_leaf check_no_agents_import check_file_sizes check_test_hygiene; do
	script="scripts/${guard}.sh"
	[[ -f "$script" ]] || fail "缺少 $script —— 守卫脚本本身丢了也要当失败处理"
	step "bash ${script#scripts/}"
	bash "$script" || fail "${guard} 棘轮退步"
done

# 并发面：这些包改了必须跑一次 -race（AGENTS.md §8.3）。
RACE_PKGS=(./internal/messaging/... ./internal/mcp/ ./internal/datamanager/ ./internal/toolspec/ ./internal/recovery/ ./internal/agents/toolbatch/ ./internal/tools/)

if [[ "$FAST" == true ]]; then
	step "go test（受影响包，--fast）"
	go test ./internal/... -count=1 || fail "单测失败"
else
	step "go test ./... （全量）"
	go test ./... -count=1 -timeout 600s || fail "单测失败"
fi

if [[ "${RACE:-false}" == true ]]; then
	step "go test -race（并发包）"
	go test -race -count=1 -timeout 600s "${RACE_PKGS[@]}" || fail "race 检查失败"
fi

step "OK：编译 / vet / 守卫 / 单测全部通过"
