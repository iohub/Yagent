#!/usr/bin/env bash
# check_no_bare_go.sh — 全仓裸 goroutine 硬门（无 PENDING 名单）
#
# 背景（docs/structured-concurrency-design.md D1）：迁移前这仓有 24 处裸 `go`，其中 12 处
# 没有 recover（两处会调 LLM —— panic 直接崩进程），而且**没有一处能被等待**：退出时
# "后台还在跑"是常态。加一层 supervise 而允许例外，等于再造一份"声明了但没人读"的账
# （AGENTS.md §11 坑 25 点名的就是这类病），所以这里刻意不留棘轮名单：
# 唯一允许写裸 `go` 的地方是 internal/supervise 自己。
#
# 规则：
#   - 扫 internal/**/*.go 与根目录 *.go；`*_test.go` 豁免（测试里的并发是表达手段，不是生命周期）。
#   - 命中 `^[缩进]go func(...) ` 或 `^[缩进]go 某方法(...)` 即失败。
#   - 只排除 internal/supervise/ 包内（它自己就是那把内核）。
#
# 用法：bash scripts/check_no_bare_go.sh
# 退出码：0 = 通过；非 0 = 出现裸 go（并点名文件:行）。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# 根目录 .go 文件 + internal 树；submodule（codeseek）与 docs 不参与。
targets=(internal)
while IFS= read -r f; do targets+=("$f"); done < <(find . -maxdepth 1 -name '*.go' -type f)

# 排除按"路径字段"来排除，不按整行子串：代码/注释里也可能出现 internal/supervise/ 这个串。
hits="$(grep -rnE '^[[:space:]]*go (func|[A-Za-z_][A-Za-z0-9_.]*\()' --include='*.go' "${targets[@]}" \
	| grep -v '_test\.go:' \
	| grep -Ev '^[^:]*internal/supervise/[^:]+\.go:[0-9]+:' || true)"

if [[ -n "$hits" ]]; then
	echo "ERROR: 发现裸 goroutine —— 它们没有名字、没有 recover、也没有人等：" >&2
	echo "$hits" | sed 's|^\./||; s/^/  /' >&2
	count="$(printf '%s\n' "$hits" | wc -l | tr -d ' ')"
	echo "" >&2
	echo "共 $count 处。正确写法（每条都要有名字，名字会出现在退出时的泄漏报告里）：" >&2
	echo "  有 ctx 的路径：supervise.From(ctx).Go(\"mcp/read-loop\", func(ctx context.Context) { ... })" >&2
	echo "  建一条子域：  grp, ctx := supervise.New(parentCtx, \"task/\"+id)；用完 grp.Drain(timeout)" >&2
	echo "  进程级后台：  supervise.Root().Go(\"datamanager/index-persist\", ...)" >&2
	exit 1
fi

echo "OK: 全仓 goroutine 都由 internal/supervise 管辖（测试文件豁免）"
exit 0
