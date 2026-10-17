package memory

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// StopFacts 是"这一轮停在哪儿、还能不能继续"的最小事实集。
//
// 只从 rollout 的**尾部**读：终止事件与检查点都由写入端保证落在文件末尾
// （两个循环都只在终止路径上写，见 docs/pause-resume-design.md §5.3），
// 所以列举会话不需要为每个文件重建整份消息序列。
//
// Found=false 表示"尾部没有终止记录"（存量文件、被截断、或进程被硬杀），
// 它**不等于**"这个会话不能继续" —— 消费方必须把它显示成未知，而不是滤掉这条。
type StopFacts struct {
	Found       bool
	Finished    bool // task_complete
	Aborted     bool // turn_aborted
	StopReason  string
	AbortReason string
	Step        int
	MaxSteps    int
	Checkpoint  *AgentCheckpoint // 无检查点记录时为 nil（崩溃 / 存量文件）
}

// 尾读窗口。第一次只读 32 KB；没读到终止记录就放大到 256 KB（末尾若是一条超大
// 的 response_item，32 KB 会整窗落在它内部）；再不行就整读。
const (
	stopTailSmall  = int64(32 << 10)
	stopTailMedium = int64(256 << 10)
)

// ReadRolloutStopFacts 读一个 rollout 文件的终止事实。
//
// 事件解码复用 rolloutParser.handleLine —— 终止事件的字段回填规则
// （Steps/MaxSteps/StopReason 的"后者胜出"与 AbortReason 的赋值条件）只该有一份，
// 在 stop 侧另写一套 JSON 解析的话，漂移的表现是判据静默读不出来，
// 而读不出来会被当成 Found=false（存量会话），比报错难查得多。
func ReadRolloutStopFacts(path string) (StopFacts, error) {
	f, err := os.Open(path)
	if err != nil {
		return StopFacts{}, fmt.Errorf("rollout stop facts: open %s: %w", path, err)
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return StopFacts{}, fmt.Errorf("rollout stop facts: stat %s: %w", path, err)
	}
	size := st.Size()

	windows := []int64{stopTailSmall, stopTailMedium}
	if size <= stopTailSmall {
		windows = []int64{size}
	}

	var last StopFacts
	for _, window := range windows {
		start := size - window
		if start < 0 {
			start = 0
		}
		lines, rerr := readTailLines(f, start, size)
		if rerr != nil {
			return StopFacts{}, fmt.Errorf("rollout stop facts: read tail of %s: %w", path, rerr)
		}
		last = scanStopFacts(lines)
		if last.Found {
			return last, nil
		}
		if start == 0 {
			break // 已经读过整个文件了
		}
	}
	return last, nil
}

// readTailLines 从 off 起读到文件末尾，丢掉第一条可能被截断的行。
// 末尾没有换行的那条保留（写入端每条记录都带 \n，缺换行只发生在进程被硬杀时，
// 而那条残缺记录 json 解不开，handleLine 会把它计入 SkippedBadLines）。
func readTailLines(f *os.File, off, size int64) ([]string, error) {
	n := size - off
	if n <= 0 {
		return nil, nil
	}
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, off); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	s := string(buf)
	if off > 0 {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			return nil, nil // 整个窗口都在一条记录内部
		}
		s = s[i+1:]
	}
	return strings.Split(strings.TrimRight(s, "\n"), "\n"), nil
}

func scanStopFacts(lines []string) StopFacts {
	p := newRolloutParser(DefaultRolloutReadOptions())
	for _, l := range lines {
		p.handleLine(l)
	}
	return StopFactsOf(&p.info)
}

// StopFactsOf 把整读（ReadRolloutMemory）得到的 RolloutInfo 汇合成同一份事实。
//
// 存在的唯一理由是分类表只能有一份：整读路径（Enter 之后、恢复侧）与尾读路径
// （列表）必须对同一个文件给出同一个答案，否则的表现是"列表说可原样重放、
// 弹框说不能"。
func StopFactsOf(info *RolloutInfo) StopFacts {
	if info == nil {
		return StopFacts{}
	}
	f := StopFacts{
		Finished:    info.Finished,
		Aborted:     info.Aborted,
		StopReason:  info.StopReason,
		AbortReason: info.AbortReason,
		Step:        info.BestStep(),
		MaxSteps:    info.MaxSteps,
		Checkpoint:  info.Checkpoint,
	}
	// 检查点也算"读到过停点"：终止事件与检查点分两条记录写，
	// 中间被硬杀的文件只有坐标没有终止事件，那仍然是一个有停点的会话。
	f.Found = f.Finished || f.Aborted || f.Checkpoint != nil
	return f
}

// ResumeState 是一个会话的续跑资格。词表刻意保持很小：
// 它能直接映射到状态符与颜色，而"能不能原样重放"这个判断只该有一个答案。
type ResumeState int

const (
	// ResumeUnknown = 尾部没有终止记录（存量 rollout / 被截断 / 硬杀）。
	// 必须与 ResumeFinished 区分：后者是"确实跑完了"，前者是"不知道"。
	ResumeUnknown ResumeState = iota
	// ResumeReplayable 允许原样继续（paused / cancelled）。
	ResumeReplayable
	// ResumeHandoff 不能原样重放，但能把已有交付交回 Director 走交接报告。
	ResumeHandoff
	// ResumeFailed 停在了真故障上（llm_error / 熔断 / panic）。
	ResumeFailed
	// ResumeFinished 正常收尾（task_complete / agent_exit / plain_text）。
	ResumeFinished
)

func (s ResumeState) String() string {
	switch s {
	case ResumeReplayable:
		return "replayable"
	case ResumeHandoff:
		return "handoff"
	case ResumeFailed:
		return "failed"
	case ResumeFinished:
		return "finished"
	default:
		return "unknown"
	}
}

// State 把终止事实翻成续跑资格 + 一句人话原因（可原样继续时原因为空）。
//
// **默认必须是"不可原样重放"**：反过来写会让将来新增的任何停止原因自动获得重放资格，
// 而那正是把一次失败当成可继续、进而重复执行副作用的路径。
func (f StopFacts) State() (ResumeState, string) {
	if !f.Found {
		return ResumeUnknown, "no terminal record in this rollout (legacy, truncated, or hard-killed)"
	}
	if f.Finished {
		return ResumeFinished, "the run completed"
	}
	// 本次改动之前写的 rollout 没有 stop_reason。按最保守的"取消"归类：
	// 它至少允许用户试着继续，而不是把存量会话一律判成不可恢复。
	kind := f.StopReason
	if kind == "" {
		kind = StopKindCancelled
	}
	switch kind {
	case StopKindPaused, StopKindCancelled:
		return ResumeReplayable, ""
	case StopKindMaxSteps:
		return ResumeHandoff, "run exhausted its max steps; replaying it would do the same"
	case StopKindIdleTimeout, StopKindTotalTimeout:
		return ResumeHandoff, "timed out: the interrupted step may already be recorded in the rollout"
	case StopKindLLMError, StopKindCircuitBreakerOpen, StopKindPanic:
		return ResumeFailed, "the run stopped on a real failure, not on a stop request"
	case StopKindPlainText, StopKindAgentExit:
		return ResumeFinished, "the run actually finished; nothing to continue"
	case StopKindUnaccounted:
		// "这一轮结束了，但没有任何出口认领原因"。它比 llm_error 更可疑，
		// 所以只能走交接：原样重放一个连停在哪都不知道的步骤，就是重复执行副作用。
		return ResumeHandoff, "no exit claimed this stop reason; the interrupted step is unknown"
	}
	return ResumeHandoff, "unknown stop reason " + kind
}
