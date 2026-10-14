package agents

// resume_test.go — P0.4 分类器与恢复候选的闸门（docs/pause-resume-design.md §5.4、§10 G6）。
//
// executor_stopkind_test.go 钉的是"循环真的把对的分类写进 rollout"；本文件钉的是
// "分类规则本身"，两边共用同一份词表。特别注意每条断言都必须是错误实现
// **给不出**的值 —— 否则它只是又一次"跑一遍看没报错"。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"yagent/internal/agents/activity"
	"yagent/internal/agents/director"
	"yagent/internal/llm"
	"yagent/internal/memory"
	"yagent/internal/pause"
)

// TestStopKindVocabularyIsTheRolloutContract 把落在盘上的字符串钉死。
// agents 侧常量是 memory 的别名，所以本测试约束的其实是 memory 的字面量：
// 改名不会让测试变绿，只会让存量 rollout 与 TUI 文案对不上号。
func TestStopKindVocabularyIsTheRolloutContract(t *testing.T) {
	want := map[string]string{
		StopKindPaused:             "paused",
		StopKindCancelled:          "cancelled",
		StopKindIdleTimeout:        "idle_timeout",
		StopKindTotalTimeout:       "total_timeout",
		StopKindMaxSteps:           "max_steps",
		StopKindLLMError:           "llm_error",
		StopKindCircuitBreakerOpen: "circuit_breaker_open",
		StopKindPanic:              "panic",
		StopKindPlainTextReply:     "plain_text",
		StopKindAgentExit:          "agent_exit",
		StopKindUnaccounted:        "unaccounted",
	}
	for got, expected := range want {
		if got != expected {
			t.Errorf("stop kind = %q, want %q（这是 rollout 的 on-disk 契约）", got, expected)
		}
	}
}

// TestClassifyStop_PrefersTheCauseOverTheBareCancel 是整套分类的承重墙。
//
// activity 用 WithCancelCause 注入 IdleTimeoutError，而引擎/工具看到的是裸
// context.Canceled。谁先看那个裸错误，谁就把每一次委派超时都误报成用户取消
// ——而用户取消可以原样重放、超时不行（那一步可能已经把失败结果记进了 rollout，
// 重放就是重复执行副作用）。
func TestClassifyStop_PrefersTheCauseOverTheBareCancel(t *testing.T) {
	base, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()

	watched, cancelWatch := activity.WithIdleTimeout(base, activity.Options{
		IdleTimeout:   5 * time.Millisecond,
		CheckInterval: time.Millisecond,
	})
	defer cancelWatch()

	// 等确定性信号（监视器真的触发了），不等时长。
	deadline := time.Now().Add(2 * time.Second)
	for watched.Err() == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if watched.Err() == nil {
		t.Fatal("夹具失效：activity 监视器没有触发")
	}
	var idleErr *activity.IdleTimeoutError
	if !errors.As(context.Cause(watched), &idleErr) {
		t.Fatalf("context.Cause = %T, want *activity.IdleTimeoutError", context.Cause(watched))
	}

	// 调用点递过来的是循环看到的那个裸取消 —— 只有回头看 cause 才分得对。
	if got, _ := classifyStop(context.Canceled, watched); got != StopKindIdleTimeout {
		t.Errorf("classifyStop(裸取消, 超时ctx) = %q, want %q（cause 优先级断了）", got, StopKindIdleTimeout)
	}
	if got, _ := classifyStop(nil, watched); got != StopKindIdleTimeout {
		t.Errorf("classifyStop(nil, 超时ctx) = %q, want %q", got, StopKindIdleTimeout)
	}
}

func TestClassifyStop_Table(t *testing.T) {
	canceled, cancelCanceled := context.WithCancel(context.Background())
	cancelCanceled()
	defer cancelCanceled()

	expired, cancelExpired := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancelExpired()
	<-expired.Done()

	live := context.Background()

	cases := []struct {
		name string
		err  error
		ctx  context.Context
		want string
	}{
		{"活着的 ctx 且无错误", nil, live, ""},
		{"真实失败", errors.New("500 upstream"), live, StopKindLLMError},
		{"裸取消", context.Canceled, live, StopKindCancelled},
		{"取消掉的 ctx 且无错误", nil, canceled, StopKindCancelled},
		// ctx 死了但错误本身不是取消：保留真实失败。首因规则会让先记下的取消胜出，
		// 所以这里不该为了"ctx 死了"就抹掉一个 400。
		{"ctx 死了但错误是别的", errors.New("400 bad request"), canceled, StopKindLLMError},
		{"裸 deadline", context.DeadlineExceeded, live, StopKindTotalTimeout},
		{"过期的 ctx", nil, expired, StopKindTotalTimeout},
		{"包了一层的取消", fmt.Errorf("delegate failed: %w", context.Canceled), live, StopKindCancelled},
		{"包了一层的空闲超时", &wrapped{err: &activity.IdleTimeoutError{IdleFor: time.Minute}}, live, StopKindIdleTimeout},
		{"包了一层的总时长超限", &wrapped{err: &activity.TotalTimeoutError{Limit: time.Hour}}, live, StopKindTotalTimeout},
		{"nil ctx 不许 panic", errors.New("boom"), nil, StopKindLLMError},
		// 闸门（A 层）：停在 step 边界上后被取消 = paused，与"停在工具中途"的
		// cancelled 分开 —— P2 的重放判据靠这个差别决定敢不敢原样继续。
		{
			"停在闸门上后被取消",
			&pause.AbandonedError{Reason: "/pause", Cause: context.Canceled},
			canceled,
			StopKindPaused,
		},
		{
			"闸门包着裸取消（ctx 还活着）",
			&pause.AbandonedError{Reason: "/pause", Cause: context.Canceled},
			live,
			StopKindPaused,
		},
		// 一个包着超时的 AbandonedError，真实原因是超时而不是"用户停住了"：
		// 超时那一步可能已把失败结果记进 rollout，重放会重复副作用。
		{
			"闸门包着空闲超时",
			&pause.AbandonedError{Cause: &activity.IdleTimeoutError{IdleFor: time.Minute, Threshold: time.Minute}},
			live,
			StopKindIdleTimeout,
		},
		{
			"闸门包着 deadline",
			&pause.AbandonedError{Cause: context.DeadlineExceeded},
			live,
			StopKindTotalTimeout,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := classifyStop(tc.err, tc.ctx)
			if got != tc.want {
				t.Errorf("classifyStop(%v, ctx) = %q, want %q", tc.err, got, tc.want)
			}
			theirs := director.StopKindOf(tc.err, tc.ctx)
			// 两边对"没有任何信号"的处理本来就不同：agents 返回空串让调用点自己决定，
			// director 直接写账本所以必须有个词。除这一对外必须完全一致。
			wantTheirs := tc.want
			if wantTheirs == "" {
				wantTheirs = StopKindUnaccounted
			}
			if theirs != wantTheirs {
				t.Errorf("两份实现漂移: agents=%q director=%q, want director=%q", got, theirs, wantTheirs)
			}
		})
	}
}

// wrapped 用来验证 errors.As 能穿透委派链上的包装。
type wrapped struct{ err error }

func (w *wrapped) Error() string { return "wrapped: " + w.err.Error() }
func (w *wrapped) Unwrap() error { return w.err }

// TestStopKindClassifiersAgree 是本文件存在的另一半理由：agents 与 director 各有一份
// 形态相同的实现（后者不能 import 前者），漂移的后果是同一个停止在两个包的账本里
// 留下两个名字，而恢复侧是按名字决定要不要重放的。
func TestStopKindClassifiersAgree(t *testing.T) {
	idleCause := &activity.IdleTimeoutError{IdleFor: time.Minute, Threshold: time.Minute}
	totalCause := &activity.TotalTimeoutError{Limit: time.Hour}

	causeCtx := func(cause error) context.Context {
		ctx, cancel := context.WithCancelCause(context.Background())
		t.Cleanup(func() { cancel(nil) })
		cancel(cause)
		return ctx
	}

	inputs := []struct {
		name string
		err  error
		ctx  context.Context
	}{
		{"裸取消", context.Canceled, context.Background()},
		{"真实失败", errors.New("500"), context.Background()},
		{"deadline", context.DeadlineExceeded, context.Background()},
		{"什么都没有", nil, context.Background()},
		{"cause 是 idle 而 err 是裸取消", context.Canceled, causeCtx(idleCause)},
		{"cause 是 total 而 err 是裸取消", context.Canceled, causeCtx(totalCause)},
		{"cause 是 idle 且 err 为 nil", nil, causeCtx(idleCause)},
		{"cause 就是裸取消（用户按停止）", nil, causeCtx(context.Canceled)},
		// 闸门那两类必须也进这张表：两份实现各有一份 abandoned 分支，
		// 只在一边删掉的话，同一个停止会在两份账本里留下两个名字。
		{"停在闸门上后被取消", &pause.AbandonedError{Reason: "/pause", Cause: context.Canceled}, context.Background()},
		{"闸门包着空闲超时", &pause.AbandonedError{Cause: idleCause}, context.Background()},
	}
	for _, in := range inputs {
		t.Run(in.name, func(t *testing.T) {
			mine, _ := classifyStop(in.err, in.ctx)
			theirs := director.StopKindOf(in.err, in.ctx)
			// 只有"没有任何信号"这一对允许不同：agents 返回空串让调用点自己决定，
			// director 返回 unaccounted 因为它直接写账本。
			if mine == "" {
				if theirs != StopKindUnaccounted {
					t.Fatalf("agents 说没信号时 director 必须说 unaccounted, got %q", theirs)
				}
				return
			}
			if mine != theirs {
				t.Errorf("分类漂移: agents=%q director=%q (err=%v)", mine, theirs, in.err)
			}
		})
	}
}

// TestStopKindTracker_KeepsTheDiagnosableReason 钉住 reason 文案走的是 cause 通道。
// 分类对了但文案还是引擎那句裸 "context canceled"，等于账本自相矛盾：
// stop_reason 说 idle_timeout，abort_reason 说用户取消。
func TestStopKindTracker_KeepsTheDiagnosableReason(t *testing.T) {
	cause := &activity.IdleTimeoutError{IdleFor: 90 * time.Second, Threshold: 60 * time.Second}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	cancel(cause)

	tracker := newStopKindTracker(ctx)
	tracker.record("", context.Canceled) // 循环看到的就是这个裸取消

	if got := tracker.kindOf(); got != StopKindIdleTimeout {
		t.Errorf("kindOf = %q, want %q", got, StopKindIdleTimeout)
	}
	reason := tracker.reasonText("FALLBACK")
	if reason != cause.Error() {
		t.Errorf("reasonText = %q, want the IdleTimeoutError text %q", reason, cause.Error())
	}
	if reason == "context canceled" {
		t.Error("reason 退回了裸取消：恢复侧看不出这是超时而不是用户停止")
	}

	// 没记过时要用 fallback，不能写出空字符串。
	fresh := newStopKindTracker(context.Background())
	if got := fresh.reasonText("FALLBACK"); got != "FALLBACK" {
		t.Errorf("reasonText fallback = %q, want %q", got, "FALLBACK")
	}
}

// TestScanResumeCandidates_ClassifiesEveryStopKind 覆盖 §10 G6：恢复策略的每个分支
// 都要有一个能触发它的夹具。词表里没被 switch 列出的值会落到 default（不可重放），
// 这是故意的 —— 新增停止原因时必须显式决定能不能原样继续。
func TestScanResumeCandidates_ClassifiesEveryStopKind(t *testing.T) {
	dir := filepath.Join(memory.RolloutRootDir(), "scan-stopkinds")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	kinds := []string{
		StopKindPaused, StopKindCancelled, StopKindIdleTimeout, StopKindTotalTimeout,
		StopKindMaxSteps, StopKindLLMError, StopKindCircuitBreakerOpen, StopKindPanic,
		StopKindPlainTextReply, StopKindAgentExit, StopKindUnaccounted, "brand_new_kind",
	}
	for i, kind := range kinds {
		name := fmt.Sprintf("scan-%02d", i)
		w, err := memory.NewRolloutWriter(name, "task-"+name, "scan-stopkinds")
		if err != nil || !w.Enabled() {
			t.Skipf("rollout writer unavailable: %v", err)
		}
		cwd, _ := os.Getwd()
		if err := w.WriteSessionMeta(memory.SessionMeta{ID: w.SessionID(), SessionID: w.SessionID(), Cwd: cwd}); err != nil {
			t.Fatalf("write session meta: %v", err)
		}
		// 前置条件：每个夹具必须带一条真实消息。ListRolloutSessions 会把"整文件
		// 没有 message 条目"的文件判成 Empty 直接滤掉 —— 少了这一行，扫到的是空清单，
		// 而 byKind 长度检查会把失败伪装成"扫描漏了"。
		items := memory.LLMMessageToResponseItems(llm.Message{Role: llm.RoleUser, Content: "task for " + kind}, w.NextMessageID())
		for _, item := range items {
			if err := w.WriteResponseItem(item); err != nil {
				t.Fatalf("write response item: %v", err)
			}
		}
		w.WriteRunAborted("stop reason "+kind, memory.RunState{Steps: 3, MaxSteps: 10, StopReason: kind})
		if err := w.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
	}

	candidates, err := ScanResumeCandidates(dir)
	if err != nil {
		t.Fatalf("ScanResumeCandidates: %v", err)
	}
	if len(candidates) != len(kinds) {
		t.Fatalf("扫到 %d 个候选，写了 %d 个夹具（fixture 或扫描漂移）", len(candidates), len(kinds))
	}
	byKind := map[string]ResumeCandidate{}
	for _, c := range candidates {
		byKind[c.StopKind] = c
	}

	// 默认拒绝：只有"主动停下来的"两类允许原样继续。paused 必然落在 step 边界，
	// cancelled 可能停在工具中途 —— 两者都可继续，但 P2 的重放判据要能分得开。
	resumable := map[string]bool{StopKindCancelled: true, StopKindPaused: true}
	for _, kind := range kinds {
		c, ok := byKind[kind]
		if !ok {
			t.Errorf("kind %q 没被扫到", kind)
			continue
		}
		wantResumable := resumable[kind]
		if c.Resumable != wantResumable {
			t.Errorf("%q: Resumable = %v, want %v", kind, c.Resumable, wantResumable)
		}
		if wantResumable && c.Unresumeable != "" {
			t.Errorf("%q: 可继续的候选不该带不可继续说明: %q", kind, c.Unresumeable)
		}
		if !wantResumable && c.Unresumeable == "" {
			t.Errorf("%q: 不可继续却给不出原因——用户只会看到一个没有解释的拒绝", kind)
		}
		if kind == "brand_new_kind" && c.Unresumeable != "unknown stop reason brand_new_kind" {
			t.Errorf("词表外的值必须原样报出来，got %q", c.Unresumeable)
		}
		if c.Step != 3 || c.MaxSteps != 10 {
			t.Errorf("%q: step 账目 = %d/%d, want 3/10", kind, c.Step, c.MaxSteps)
		}
	}
}
