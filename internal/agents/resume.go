package agents

// resume.go — "这一轮是怎么结束的"的分类与恢复候选扫描。
//
// 停在一个子 Agent 上之后，恢复侧要回答三件事：能不能继续、该从哪一层继续、
// 要不要告诉用户副作用可能停在半路。这三件事的判据都来自**停止原因**，
// 而此前它只有一个自由文本 reason（executor.go 的 turn_aborted 连 StopReason
// 都不填），调用方只能拿 errors.Is(err, context.Canceled) 去猜——猜不出
// "用户主动停止"与"idle 超时"的区别，而这两者的正确续跑策略正好相反：
// 前者可以原样重放，后者那一步可能已经把失败结果记进了 rollout，重放会重复副作用。
//
// 取值词表住在 memory（见 rollout_types.go 的 StopKind*），因为写入端有两个包
//（本包的 executor、director 子包的 planner）而 director 不得 import 本包。
// 本包只保留一份分类实现，director 有一份形态相同的，二者由
// TestStopKindClassifiersAgree 钉住。

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"yagent/internal/agents/activity"
	"yagent/internal/memory"
	"yagent/internal/pause"
)

// 本包使用的词表别名。必须是 const 别名而不是 var：
// 写成变量的话，"两边取值一致"这类断言会退化成两个变量互比，改一边红不了另一边。
const (
	StopKindPaused             = memory.StopKindPaused
	StopKindCancelled          = memory.StopKindCancelled
	StopKindIdleTimeout        = memory.StopKindIdleTimeout
	StopKindTotalTimeout       = memory.StopKindTotalTimeout
	StopKindMaxSteps           = memory.StopKindMaxSteps
	StopKindLLMError           = memory.StopKindLLMError
	StopKindCircuitBreakerOpen = memory.StopKindCircuitBreakerOpen
	StopKindPanic              = memory.StopKindPanic
	StopKindUnaccounted        = memory.StopKindUnaccounted
	StopKindPlainTextReply     = memory.StopKindPlainText
	StopKindAgentExit          = memory.StopKindAgentExit
)

// isSuccessKind 区分"这一轮正常做完了"与"这一轮被什么打断了"。
// 恢复侧只关心后者，而这两类推出的续跑策略完全不同，所以必须显式列举。
func isSuccessKind(kind string) bool {
	return kind == StopKindPlainTextReply || kind == StopKindAgentExit
}

// classifyStop 把一个取消/超时错误翻译成停止原因。
//
// **ctx 优先于 err，cause 优先于 ctx.Err()**，这个顺序是承重的：
// 取消路径上调用点递过来的是 ctx.Err()（裸 context.Canceled），而 activity 经
// WithCancelCause 注入的 IdleTimeoutError / TotalTimeoutError 只在 cause 通道上。
// 写成"先看 err"，委派超时会被全数误报成用户取消 —— 而用户取消可以原样重放、
// 超时不行（那一步可能已经把失败结果记进 rollout，重放会重复副作用）。
func classifyStop(err error, ctx context.Context) (string, error) {
	if ctx != nil {
		if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) && !errors.Is(cause, context.DeadlineExceeded) {
			err = cause
		} else if err == nil {
			err = ctx.Err()
		}
	}
	if err == nil {
		return "", nil
	}
	var idleErr *activity.IdleTimeoutError
	var totalErr *activity.TotalTimeoutError
	var abandoned *pause.AbandonedError
	switch {
	case errors.As(err, &idleErr):
		return StopKindIdleTimeout, err
	case errors.As(err, &totalErr):
		return StopKindTotalTimeout, err
	case errors.Is(err, context.DeadlineExceeded):
		// 委派之外也有 deadline（单工具 defaultToolTimeout / 单次 LLM llmTimeout）。
		// 归到 total 比归到 cancelled 有用：两者都不该原样重放同一步。
		// 排在 abandoned 之前：一个包着超时的 AbandonedError，真实原因是超时，
		// 不是"用户停住了"。
		return StopKindTotalTimeout, err
	case errors.As(err, &abandoned):
		// 必须排在 cancelled 之前：AbandonedError 能 errors.Is 到 context.Canceled，
		// 先写 cancelled 的话这一整类就永远不会出现。
		return StopKindPaused, err
	case errors.Is(err, context.Canceled):
		return StopKindCancelled, err
	default:
		return StopKindLLMError, err
	}
}

// stopKindTracker 记住这一轮结束的原因。规则：
//   - 失败类覆盖成功类（先记了 plain_text 再 panic，要说 panic）；
//   - 同类之间**首因胜出**（取消会连锁引发后续失败，让后写者覆盖记到的
//     永远是最后那层噪声，真正的原因反而看不见）。
type stopKindTracker struct {
	ctx     context.Context
	mu      sync.Mutex
	kind    string
	reason  string
	hasFail bool
}

func newStopKindTracker(ctx context.Context) *stopKindTracker {
	return &stopKindTracker{ctx: ctx}
}

// record 记一次结束原因。kind 已经明确的出口（panic / max_steps / 两种成功）
// 直接传它；只有裸错误可传的地方传 ""，由 classifyStop 结合 ctx 判定。
func (t *stopKindTracker) record(kind string, err error) {
	diagnostic := err
	if kind == "" {
		var classified error
		kind, classified = classifyStop(err, t.ctx)
		if classified != nil {
			diagnostic = classified
		}
	}
	if kind == "" {
		return
	}
	failing := !isSuccessKind(kind)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.kind == "" {
		t.kind = kind
		t.reason = reasonOf(diagnostic, kind)
		t.hasFail = failing
		return
	}
	if !t.hasFail && failing {
		t.kind = kind
		t.reason = reasonOf(diagnostic, kind)
		t.hasFail = true
	}
}

func reasonOf(err error, kind string) string {
	if err != nil {
		return err.Error()
	}
	return kind
}

// reasonText 给终止事件用。**分类时采用的那个错误**才是可诊断的文案：
// activity 注入的 IdleTimeoutError 自带"空闲了多久、阈值多少、最后一个动作是什么"，
// 而引擎在取消路径上返回的只是四个字 context canceled——恢复侧看到后者会以为
// 是用户按了停止，而这正是本次改动要消除的混淆。
func (t *stopKindTracker) reasonText(fallback string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.reason != "" {
		return t.reason
	}
	return fallback
}

// kind 返回记录到的停止原因。没有出口认领过时返回 unaccounted ——
// 那说明有人新加了一条 return 路径却没认领原因，这个信号必须可见而不是留空。
func (t *stopKindTracker) kindOf() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.kind == "" {
		return StopKindUnaccounted
	}
	return t.kind
}

// ResumeCandidate 是一个可以被"继续"的落点。
type ResumeCandidate struct {
	RolloutPath  string
	AgentName    string
	Step         int
	MaxSteps     int
	StopKind     string
	StopReason   string
	BoardID      string
	Resumable    bool
	Unresumeable string
}

// ScanResumeCandidates 扫描一个目录下的 rollout 文件，挑出"带着中断记录"的会话，
// 顺序继承自 ListRolloutSessions（mtime 新→旧）。
//
// 只看 turn_aborted（reader 只在见到它时置 Aborted），所以 task_complete 的会话
// 天然不在候选里。Resumable=false 的候选仍然返回：用户需要知道"这里有个被打断的
// 任务，但它不能原样继续、只能走交接"——静默把它滤掉正是 §13 禁止的那件事。
func ScanResumeCandidates(dir string) ([]ResumeCandidate, error) {
	sessions, err := memory.ListRolloutSessions(dir, 0)
	if err != nil {
		return nil, fmt.Errorf("resume: list rollout dir %s: %w", dir, err)
	}

	candidates := make([]ResumeCandidate, 0, len(sessions))
	for _, s := range sessions {
		_, info, rerr := memory.ReadRolloutMemory(s.Path, memory.DefaultRolloutReadOptions())
		// 读不出消息的文件也会带回 p.info：终止事件是独立通道，
		// Aborted 比"有没有消息"更值得信。
		if rerr != nil && !info.Aborted {
			continue
		}
		if !info.Aborted {
			continue
		}
		kind := info.StopReason
		if kind == "" {
			// 本次改动之前写的 rollout 没有 StopReason。按最保守的"取消"归类：
			// 它至少允许用户试着继续，而不是把存量会话一律判成不可恢复。
			kind = StopKindCancelled
		}
		agent := s.AgentName
		if agent == "" {
			agent = "director"
		}
		c := ResumeCandidate{
			RolloutPath: s.Path,
			AgentName:   agent,
			Step:        info.BestStep(),
			MaxSteps:    info.MaxSteps,
			StopKind:    kind,
			StopReason:  info.AbortReason,
			BoardID:     info.BoardID,
		}
		// 默认不可重放：只有"用户/系统主动停止"这一类允许原样继续。
		// 反过来写（默认允许）会让将来新增的任何停止原因自动获得重放资格，
		// 而那正是把一次失败当成可继续、进而重复执行副作用的路径。
		switch kind {
		case StopKindPaused, StopKindCancelled:
			// 两类都是"主动停下来的"，所以可以原样继续；差别在停在哪：
			// paused 必然落在 step 边界（闸门只在批次之后生效），
			// cancelled 可能停在某个在飞的工具中间 —— P2 的重放判据用得上。
			c.Resumable = true
		case StopKindMaxSteps:
			c.Unresumeable = "run exhausted its max steps; replaying it would do the same"
		case StopKindIdleTimeout, StopKindTotalTimeout:
			c.Unresumeable = "timed out: the interrupted step may already be recorded in the rollout"
		case StopKindLLMError, StopKindCircuitBreakerOpen, StopKindPanic:
			c.Unresumeable = "the run stopped on a real failure, not on a stop request"
		case StopKindPlainTextReply, StopKindAgentExit:
			c.Unresumeable = "the run actually finished; nothing to continue"
		default:
			c.Unresumeable = "unknown stop reason " + kind
		}
		candidates = append(candidates, c)
	}
	return candidates, nil
}
