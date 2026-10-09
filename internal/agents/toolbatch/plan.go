// Package toolbatch —— 工具调用"顺序保持的分段并发"调度器（叶子包）。
//
// 本包是纯调度器：不感知任何业务语义（不 import llm/tools 等业务包），
// 只负责把"单步 LLM 返回的若干 tool_calls"按调用方给定的并行性谓词分段：
//
//   - 连续的可并行元素聚合为一个"只读游程组"，组内并发执行（信号量限流 +
//     结果槽 + WaitGroup），join 后按原索引序逐个 commit；
//   - 非并行元素作为单元素串行段原位执行；
//   - 组大小为 1 或并行度 ≤1 时走无 goroutine 的串行快路径（串行段、单元素
//     组、并行度 1 三者执行路径统一，与逐元素串行完全一致）。
//
// 可观察时序语义（messages/rollout/回调顺序）由调用方在 commit 回调里保证：
// Run 保证 onCommit 的调用发生在 join 之后，父线程单线程、按原索引序。
package toolbatch

// Segment 描述一段调度单元：
//   - Parallel=true  表示"只读游程组"，Indices 为组内元素的原索引（升序、连续）；
//   - Parallel=false 表示"必须原位串行执行的非并行元素"，Indices 恒为单元素。
//
// deny-list（如 agent_exit / delegate_meta）由调用方在并行性谓词中保证，
// Plan 本身不感知具体工具名。
type Segment struct {
	Parallel bool
	Indices  []int
}

// Plan 把 total 个元素（索引 0..total-1）按 isParallelizable 谓词切分为调度段：
// 连续的可并行元素聚合为并行段；每个不可并行元素单独成串行段。
// 线性扫描、稳定保序：返回段的前后顺序即元素原顺序。
// isParallelizable 为 nil 时所有元素视为不可并行（fail-safe）。
func Plan(total int, isParallelizable func(i int) bool) []Segment {
	if total <= 0 {
		return nil
	}
	var segs []Segment
	var run []int

	flushParallel := func() {
		if len(run) > 0 {
			segs = append(segs, Segment{Parallel: true, Indices: run})
			run = nil
		}
	}

	for i := 0; i < total; i++ {
		if isParallelizable != nil && isParallelizable(i) {
			run = append(run, i)
			continue
		}
		flushParallel()
		segs = append(segs, Segment{Parallel: false, Indices: []int{i}})
	}
	flushParallel()

	return segs
}
