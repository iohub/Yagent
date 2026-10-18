package tui

// tui_resume_stop.go — 会话清单的"能不能续、停在哪"（docs/resume-tui-redesign.md §3，R2）。
//
// 判据走 memory.ReadRolloutStopFacts（尾读），不走 ReadRolloutMemory（整读）：
// 清单要的是每条文件的一小段终止事实，整读会把 O(总字节) 摊到每次 /resume 上。
// 恢复侧仍然整读，两边共用 memory 那张分类表，所以对同一个文件不会给出矛盾答案。

import (
	"context"
	"image/color"
	"sort"
	"sync"

	"yagent/internal/memory"
	"yagent/internal/supervise"

	"charm.land/lipgloss/v2"
)

// resumeStopIndex 按 rollout 路径索引终止事实。
type resumeStopIndex map[string]memory.StopFacts

// stopScanConcurrency 是尾读的并发上限。用 WaitGroup +  buffered channel 而不是
// errgroup：golang.org/x/sync 在 go.mod 里是 indirect，为这点便利提成直接依赖不值当。
// WaitGroup 也留着没换成 supervise.Drain —— 这一批必须等**自己这几个**，
// 而 Drain 等的是整条域（包含别的后台工作），语义不对。supervise 在这里只给名字和 recover。
const stopScanConcurrency = 8

// readResumeStopFacts 并发尾读每个文件的终止事实。
//
// 单个文件读失败不进索引（等价于"未知"），不整体失败 —— 清单的价值是
// "把所有会话列出来并标出可续的"，一条文件坏了不能让整屏空白。
func readResumeStopFacts(items []memory.RolloutSessionInfo) resumeStopIndex {
	out := make(resumeStopIndex, len(items))
	if len(items) == 0 {
		return out
	}

	var mu sync.Mutex
	sem := make(chan struct{}, stopScanConcurrency)
	var wg sync.WaitGroup
	for _, it := range items {
		path := it.Path
		wg.Add(1)
		supervise.Root().Go("tui/resume-tail-read", func(context.Context) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			facts, err := memory.ReadRolloutStopFacts(path)
			if err != nil {
				return
			}
			mu.Lock()
			out[path] = facts
			mu.Unlock()
		})
	}
	wg.Wait()
	return out
}

// resumeStateRank 是排序优先级：可原样重放最前，跑完的沉底。
// 未知（存量/被硬杀）排在交接之后、失败之前 —— 它是"不知道"，不是"不能"。
func resumeStateRank(f memory.StopFacts) int {
	state, _ := f.State()
	switch state {
	case memory.ResumeReplayable:
		return 0
	case memory.ResumeHandoff:
		return 1
	case memory.ResumeUnknown:
		return 2
	case memory.ResumeFailed:
		return 3
	default:
		return 4
	}
}

// groupRank 取一组（主会话 + 它的子 Agent 文件）里最好的那一档：
// 主会话判成"跑完了"而某个子文件还带着可重放的停点时，这组仍然值得先看到。
func groupRank(index resumeStopIndex, head memory.RolloutSessionInfo, members []memory.RolloutSessionInfo) int {
	best := 5
	for _, it := range append([]memory.RolloutSessionInfo{head}, members...) {
		if f, ok := index[it.Path]; ok && resumeStateRank(f) < best {
			best = resumeStateRank(f)
		}
	}
	if best == 5 {
		return 2 // 一条判据都没读到 = 未知
	}
	return best
}

// sortResumeGroupsByState 把"值得续的"排到前面，同档内保持 buildResumeGroups
// 给的 mtime 新→旧顺序（稳定排序：这一档内部不许再洗牌，否则每次 /resume
// 看到的顺序都不同，用户没法按"第几行"记忆自己的会话）。
func sortResumeGroupsByState(groups []resumeGroup, index resumeStopIndex) []resumeGroup {
	sort.SliceStable(groups, func(i, j int) bool {
		ri := groupRank(index, groups[i].head, groups[i].members)
		rj := groupRank(index, groups[j].head, groups[j].members)
		return ri < rj
	})
	return groups
}

// 行首那两格的词表。
//
// 词表刻意只用单格宽的字：`●`/`○` 与仓库其它表面同源，`!`/`?`/`·` 是 ASCII/拉丁点。
// 禁 Emoji_Presentation 或 EA=W 的字（`⏸` U+23F8、`✗` U+2717 在某些字体里是歧义宽，
// `⚡` U+26A1 多数终端占两格）—— 一个歧义宽度字符就能把固定列整行顶歪。
func resumeStateGlyph(state memory.ResumeState) string {
	switch state {
	case memory.ResumeReplayable:
		return "●" // 实心 = 有停点、可原样重放
	case memory.ResumeHandoff:
		return "○" // 空心 = 有停点，但只能走交接
	case memory.ResumeFailed:
		return "!" // 停在真故障上
	case memory.ResumeFinished:
		return "·" // 正常收尾，没有停点
	default:
		return "?" // 读不到判据 = 不知道，不是不能
	}
}

func resumeStateColor(state memory.ResumeState) color.Color {
	switch state {
	case memory.ResumeReplayable:
		return lipgloss.Color("114") // Success
	case memory.ResumeHandoff:
		return lipgloss.Color("214") // Accent
	case memory.ResumeFailed:
		return lipgloss.Color("196") // Danger
	case memory.ResumeFinished:
		return lipgloss.Color("240") // Border
	default:
		return lipgloss.Color("244") // TextMuted
	}
}

// resumableGroups 是顶栏 meta 的账目：有几**组**（= 几个任务）能原样接着跑。
//
// 分子分母必须是同一类东西。按文件计数会让"6 条里 1 条可续"里的 6 变成
// 展开后的行数（主会话 + 子 Agent 文件），而用户数的"条"是任务。
func resumableGroups(index resumeStopIndex, groups []resumeGroup) int {
	n := 0
	for _, g := range groups {
		if groupRank(index, g.head, g.members) == 0 {
			n++
		}
	}
	return n
}
