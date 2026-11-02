package agents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yagent/internal/agents/activity"
	"yagent/internal/agents/director"
	"yagent/internal/llm"
	"yagent/internal/logging"
	"yagent/internal/memory"
	"yagent/internal/tools"
	"yagent/internal/toolspec"
)

type b3Rec struct {
	mu     sync.Mutex
	seq    []string
	invoke atomic.Int64
}

func (r *b3Rec) append(elem string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq = append(r.seq, elem)
}

func (r *b3Rec) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.seq...)
}

type b3Bus struct {
	spy *spyPublisher
	rec *b3Rec
}

var _ EventBus = (*b3Bus)(nil)

func (b *b3Bus) Publish(eventType string, content interface{}, from string) error {
	b.rec.append("event:" + eventType)
	return b.spy.Publish(eventType, content, from)
}

func (b *b3Bus) PublishWithMetadata(eventType string, content interface{}, from string, metadata map[string]interface{}) error {
	b.rec.append("event:" + eventType)
	return b.spy.PublishWithMetadata(eventType, content, from, metadata)
}

type b3Behavior string

const (
	b3OK      b3Behavior = "ok"
	b3Fail    b3Behavior = "fail"
	b3Outcome b3Behavior = "outcome"
	b3Block   b3Behavior = "block"
	b3Hang    b3Behavior = "hang"
)

type b3Probe struct {
	rec      *b3Rec
	behavior b3Behavior
	pool     chan struct{}
}

func (p b3Probe) observeCall(ctx context.Context, name string) {
	p.rec.invoke.Add(1)
	p.rec.append("call:" + name)
	p.rec.append("deadline:" + b3DeadlineLabel(ctx))
	p.rec.append("carrier:" + b3YesNo(b3HasOutcomeCarrier(ctx)))
	p.rec.append("delegid:" + b3DelegationIDLabel(ctx))
	if strings.HasPrefix(name, "delegate_") {
		p.rec.append("pool:" + p.poolLabel())
	}
}

func (p b3Probe) adapterFunc(name, desc string, fn tools.ToolFunc) *tools.Adapter {
	return tools.NewAdapter(name, desc, func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		p.observeCall(ctx, name)
		return fn(ctx, params)
	})
}

func (p b3Probe) adapter(name, desc string) *tools.Adapter {
	return p.adapterFunc(name, desc, func(ctx context.Context, _ map[string]interface{}) (interface{}, error) {
		switch p.behavior {
		case b3Fail:
			return nil, errors.New("b3 simulated tool failure")
		case b3Block:

			select {
			case <-ctx.Done():
			case <-time.After(10 * time.Second):
				return nil, errors.New("b3: activity monitor never fired")
			}
			p.rec.append("heartbeat:" + b3KindOfCause(ctx))
			return nil, ctx.Err()
		case b3Hang:

			select {
			case <-ctx.Done():
			case <-time.After(6 * time.Second):
				return nil, errors.New("b3: tool-level deadline never fired")
			}
			return nil, ctx.Err()
		case b3Outcome:

			recordDelegationOutcome(ctx, AgentResult{Text: "b3 sub deliverable", Summary: "b3 sub summary"})
			return map[string]interface{}{"summary": "b3 sub summary"}, nil
		default:
			return "b3 payload", nil
		}
	}).WithReadOnly(true)
}

func (p b3Probe) poolLabel() string {
	if p.pool == nil {
		return "none"
	}
	if len(p.pool) > 0 {
		return "held"
	}
	return "free"
}

func (p b3Probe) productionExitAdapter(t *testing.T) *tools.Adapter {
	t.Helper()
	flow := tools.NewFlowControlTool(t.TempDir())
	return p.adapterFunc("agent_exit", "production agent_exit", flow.ExecuteAgentExit)
}

func b3DeadlineLabel(ctx context.Context) string {
	d, ok := ctx.Deadline()
	if !ok {
		return "none"
	}
	rem := time.Until(d)
	if rem < time.Second {
		return "subsecond"
	}
	return fmt.Sprintf("%ds", int(rem.Round(time.Second)/time.Second))
}

func b3HasOutcomeCarrier(ctx context.Context) bool {
	_, ok := ctx.Value(delegationOutcomeKey{}).(*delegationOutcome)
	return ok
}

func b3DelegationIDLabel(ctx context.Context) string {
	if memory.DelegationIDFrom(ctx) == "" {
		return "none"
	}
	return "injected"
}

func b3KindOfCause(ctx context.Context) string {
	var idle *activity.IdleTimeoutError
	if errors.As(context.Cause(ctx), &idle) {
		return idle.LastActivityKind
	}
	return "no-idle-cause"
}

func b3YesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func b3ResultShape(s string) string {
	switch {
	case s == "":
		return "empty"
	case strings.HasPrefix(s, "Tool ") && strings.HasSuffix(s, " not found"):
		return "notfound"
	case strings.HasPrefix(s, "Error: schema for "):
		return "teach-cold"
	case strings.HasPrefix(s, "Error: "):
		return "error-text"
	default:
		return "text"
	}
}

func b3ErrLabel(err error) string {
	if err == nil {
		return "none"
	}
	return "some"
}

func b3TimeoutSemantics(result string, err error) string {
	const marker = "timed out"
	inText := strings.Contains(result, marker)
	inErr := err != nil && strings.Contains(err.Error(), marker)
	switch {
	case inText && inErr:
		return "text+error"
	case inText:
		return "text"
	case inErr:
		return "error"
	default:
		return "absent"
	}
}

func b3DeadlineSentinel(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	default:
		return "rewritten"
	}
}

func restoreDirectorToolTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := directorToolTimeout
	directorToolTimeout = d
	t.Cleanup(func() { directorToolTimeout = old })
}

const b3ToolTimeoutSeam = 60 * time.Millisecond

func b3TimeoutElements(rec *b3Rec, sc b3Scenario, result string, err error) {
	if sc.behavior != b3Hang {
		return
	}
	rec.append("timeout:" + b3TimeoutSemantics(result, err))
	rec.append("sentinel:" + b3DeadlineSentinel(err))
}

var b3LogIdentities = []string{b3ExecutorAgent, "director"}

const (
	b3ExecutorAgent = "b3-subagent"
)

func b3ToolLogLines(toolName string) int {
	path := filepath.Join(logging.GetTaskLogDir(logging.GetCurrentTaskID()),
		fmt.Sprintf("tool-%s.log", time.Now().Format("2006-01-02")))
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n := 0
	for _, who := range b3LogIdentities {
		n += strings.Count(string(b), "tool="+toolName+" agent="+who+" ")
	}
	return n
}

type b3RepoCtx struct {
	mu     sync.Mutex
	value  string
	writes int
}

var _ RepoContextStore = (*b3RepoCtx)(nil)

func (r *b3RepoCtx) Get() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.value
}

func (r *b3RepoCtx) Set(summary string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.value = summary
	r.writes++
}

func (r *b3RepoCtx) snapshot() (string, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.value, r.writes
}

const (
	b3IdleTimeout   = 100 * time.Millisecond
	b3CheckInterval = 2 * time.Millisecond
)

type b3Monitor struct {
	ctx context.Context
}

func (m *b3Monitor) awaitKind(t *testing.T) string {
	t.Helper()
	select {
	case <-m.ctx.Done():
		var idle *activity.IdleTimeoutError
		cause := context.Cause(m.ctx)
		if !errors.As(cause, &idle) {
			t.Fatalf("监视器取消原因不是 *activity.IdleTimeoutError（%v），心跳观测通道失效", cause)
		}
		return idle.LastActivityKind
	case <-time.After(10 * time.Second):
		t.Fatalf("activity 监视器没有触发，心跳观测通道失效")
		return ""
	}
}

func b3ContextFor(t *testing.T, sc b3Scenario) (context.Context, context.CancelFunc, *b3Monitor) {
	t.Helper()
	if !sc.monitored {
		return context.Background(), func() {}, nil
	}
	ctx, cancel := activity.WithIdleTimeout(context.Background(), activity.Options{
		IdleTimeout:   b3IdleTimeout,
		CheckInterval: b3CheckInterval,
	})
	return ctx, cancel, &b3Monitor{ctx: ctx}
}

const b3PickCold = "\x00pick-cold"

type b3Scenario struct {
	id        string
	tool      string
	args      string
	behavior  b3Behavior
	deferred  bool
	monitored bool
	realExit  bool
	callOnly  bool

	wantExecutor []string
	wantDirector []string
}

var b3Scenarios = []b3Scenario{
	{
		id:       "readonly",
		tool:     "b3_probe_ro",
		args:     "{}",
		behavior: b3OK,
		wantExecutor: []string{
			"event:tool_call_start", "call:b3_probe_ro", "deadline:180s",
			"carrier:no", "delegid:none", "log:wrote", "ret:text", "err:none",
		},
		wantDirector: []string{
			"call:b3_probe_ro", "deadline:120s",
			"carrier:yes", "delegid:none", "log:none", "ret:text", "err:none",
		},
	},
	{
		id:       "tool-error",
		tool:     "b3_probe_fail",
		args:     "{}",
		behavior: b3Fail,
		wantExecutor: []string{
			"event:tool_call_start", "call:b3_probe_fail", "deadline:180s",
			"carrier:no", "delegid:none", "log:wrote", "ret:error-text", "err:some",
		},
		wantDirector: []string{
			"call:b3_probe_fail", "deadline:120s",
			"carrier:yes", "delegid:none", "log:none", "ret:empty", "err:some",
		},
	},
	{
		id:       "timeout-expiry",
		tool:     "b3_probe_hang",
		args:     "{}",
		behavior: b3Hang,
		wantExecutor: []string{
			"event:tool_call_start", "call:b3_probe_hang", "deadline:subsecond",
			"carrier:no", "delegid:none", "log:wrote", "ret:error-text", "err:some",
			"timeout:text", "sentinel:deadline",
		},
		wantDirector: []string{
			"call:b3_probe_hang", "deadline:subsecond",
			"carrier:yes", "delegid:none", "log:none", "ret:empty", "err:some",
			"timeout:error", "sentinel:rewritten",
		},
	},
	{
		id:       "unknown-name",
		tool:     "b3_probe_unregistered",
		args:     "{}",
		behavior: b3OK,
		callOnly: true,

		wantExecutor: []string{"event:tool_call_start", "log:none", "ret:empty", "err:none"},
		wantDirector: []string{"log:none", "ret:notfound", "err:none"},
	},
	{
		id:       "agent-exit",
		tool:     "agent_exit",
		args:     `{"reason":"b3 contract probe"}`,
		realExit: true,
		wantExecutor: []string{
			"event:tool_call_start", "call:agent_exit", "deadline:180s",
			"carrier:no", "delegid:none", "log:wrote", "ret:text", "err:none",
		},
		wantDirector: []string{
			"call:agent_exit", "deadline:120s",
			"carrier:yes", "delegid:none", "log:none", "ret:text", "err:none",
		},
	},
	{
		id:           "cold-tool",
		tool:         b3PickCold,
		args:         "{}",
		deferred:     true,
		wantExecutor: []string{"event:tool_call_start", "log:none", "ret:teach-cold", "err:none"},
		wantDirector: []string{"log:none", "ret:teach-cold", "err:none"},
	},
	{
		id:       "interactive",
		tool:     "ask_user_for_help",
		args:     "{}",
		behavior: b3OK,
		wantExecutor: []string{
			"event:tool_call_start", "call:ask_user_for_help", "deadline:none",
			"carrier:no", "delegid:none", "log:wrote", "ret:text", "err:none",
		},
		wantDirector: []string{
			"call:ask_user_for_help", "deadline:none",
			"carrier:yes", "delegid:none", "log:none", "ret:text", "err:none",
		},
	},
	{
		id:       "delegate",
		tool:     "delegate_repo",
		args:     `{"task":"b3"}`,
		behavior: b3Outcome,
		wantExecutor: []string{
			"event:tool_call_start", "call:delegate_repo", "deadline:180s",
			"carrier:no", "delegid:none", "pool:none", "log:wrote",
			"meminject:absent", "deleg:absent", "repoctx:absent", "ret:text", "err:none",
		},
		wantDirector: []string{
			"call:delegate_repo", "deadline:none",
			"carrier:yes", "delegid:injected", "pool:held", "log:none",
			"meminject:+1", "deleg:+1", "repoctx:set", "ret:text", "err:none",
		},
	},
	{
		id:        "hb-at-exec",
		tool:      "b3_probe_hb_block",
		args:      "{}",
		behavior:  b3Block,
		monitored: true,
		wantExecutor: []string{
			"event:tool_call_start", "call:b3_probe_hb_block", "deadline:180s",
			"carrier:no", "delegid:none", "heartbeat:tool:start:b3_probe_hb_block",
			"log:wrote", "ret:error-text", "err:some",
		},
		wantDirector: []string{
			"call:b3_probe_hb_block", "deadline:120s",
			"carrier:yes", "delegid:none", "heartbeat:start",
			"log:none", "ret:empty", "err:some",
		},
	},
	{
		id:        "hb-after-done",
		tool:      "b3_probe_hb_done",
		args:      "{}",
		behavior:  b3OK,
		monitored: true,
		wantExecutor: []string{
			"event:tool_call_start", "call:b3_probe_hb_done", "deadline:180s",
			"carrier:no", "delegid:none", "log:wrote", "ret:text", "err:none",
			"heartbeat:tool:done:b3_probe_hb_done",
		},
		wantDirector: []string{
			"call:b3_probe_hb_done", "deadline:120s",
			"carrier:yes", "delegid:none", "log:none", "ret:text", "err:none",
			"heartbeat:start",
		},
	},
}

func b3ToolCall(id, name, args string) llm.ToolCall {
	return llm.ToolCall{
		ID:       id,
		Type:     "function",
		Function: llm.FunctionCall{Name: name, Arguments: args},
	}
}

func b3FirstColdToolName(s *tools.Surface) string {
	for _, n := range toolspec.Names() {
		if _, st := s.Lookup(n); st == tools.StateColdKnown {
			return n
		}
	}
	return ""
}

func b3BuildSurface(t *testing.T, sc b3Scenario, probe b3Probe) (*tools.Surface, string) {
	t.Helper()
	if sc.deferred {
		s := surfaceFor(toolspec.ProfileDirector, toolspec.ProfileDirector.ProbeDeps(), "b3-probe")
		name := b3FirstColdToolName(s)
		if name == "" {
			t.Fatalf("场景 %s 需要 deferred 面里有冷工具，实际一个都没有（画像或预算变了？先看 Surface.Skipped()）", sc.id)
		}
		return s, name
	}
	var adapters []*tools.Adapter
	switch {
	case sc.realExit:

		adapters = []*tools.Adapter{probe.productionExitAdapter(t)}
	case sc.callOnly:

		adapters = []*tools.Adapter{probe.adapter("b3_probe_sibling", "sibling tool that is on the surface")}
	default:
		adapters = []*tools.Adapter{probe.adapter(sc.tool, "b3 contract probe tool")}
	}
	return tools.NewSurfaceFromAdapters("b3-probe", adapters...), sc.tool
}

type b3SideRun struct {
	tool    string
	seq     []string
	result  string
	execErr error
	found   bool
	invoked int64
	events  int
}

func b3AppendLogPhase(rec *b3Rec, wroteLog bool) {
	if wroteLog {
		rec.append("log:wrote")
		return
	}
	rec.append("log:none")
}

func b3AppendDelegatePost(rec *b3Rec, tool string, memDelta, delegDelta *int, repoSet *bool) {
	if !strings.HasPrefix(tool, "delegate_") {
		return
	}
	rec.append("meminject:" + b3DeltaLabel(memDelta))
	rec.append("deleg:" + b3DeltaLabel(delegDelta))
	rec.append("repoctx:" + b3BoolLabel(repoSet))
}

func b3DeltaLabel(v *int) string {
	if v == nil {
		return "absent"
	}
	return fmt.Sprintf("%+d", *v)
}

func b3BoolLabel(v *bool) string {
	if v == nil {
		return "absent"
	}
	if *v {
		return "set"
	}
	return "unchanged"
}

func b3RunExecutorSide(t *testing.T, sc b3Scenario) b3SideRun {
	t.Helper()
	rec := &b3Rec{}
	bus := &b3Bus{spy: &spyPublisher{}, rec: rec}
	probe := b3Probe{rec: rec, behavior: sc.behavior}
	surface, used := b3BuildSurface(t, sc, probe)

	cfg := DefaultExecutorConfig()
	cfg.AgentName = b3ExecutorAgent
	cfg.Surface = surface
	cfg.Publisher = bus

	if sc.behavior == b3Hang {
		restoreToolTimeout(t, b3ToolTimeoutSeam)
	}

	ctx, cancel, mon := b3ContextFor(t, sc)
	defer cancel()

	logBefore := b3ToolLogLines(used)
	res, found, err := runToolCall(ctx, &cfg, b3ToolCall("call_"+sc.id, used, sc.args))
	wroteLog := b3ToolLogLines(used) > logBefore

	b3AppendLogPhase(rec, wroteLog)
	b3AppendDelegatePost(rec, used, nil, nil, nil)
	rec.append("ret:" + b3ResultShape(res))
	rec.append("err:" + b3ErrLabel(err))
	b3TimeoutElements(rec, sc, res, err)
	if mon != nil && sc.behavior != b3Block {
		rec.append("heartbeat:" + mon.awaitKind(t))
	}

	ev := 0
	for _, e := range bus.spy.snapshot() {
		if e.evType == "tool_call_start" {
			ev++
		}
	}
	return b3SideRun{tool: used, seq: rec.snapshot(),
		result: res, execErr: err, found: found, invoked: rec.invoke.Load(), events: ev}
}

func b3RunDirectorSide(t *testing.T, sc b3Scenario) b3SideRun {
	t.Helper()
	rec := &b3Rec{}
	repo := &b3RepoCtx{}
	agent := &DirectorAgent{
		repoCtx:                  repo,
		currentMemory:            memory.NewConversationMemory(64),
		delegateIdleTimeout:      30 * time.Second,
		maxParallelReadOnlyTools: NormalizeMaxParallelReadOnlyTools(4),
		maxParallelDelegations:   2,
	}
	state := &director.RunState{MaxSteps: 4}
	runner := newDirectorToolRunner(agent, state)

	if sc.behavior == b3Hang {
		restoreDirectorToolTimeout(t, b3ToolTimeoutSeam)
	}

	probe := b3Probe{rec: rec, behavior: sc.behavior, pool: runner.delegateSem}
	surface, used := b3BuildSurface(t, sc, probe)
	agent.surface = surface

	ctx, cancel, mon := b3ContextFor(t, sc)
	defer cancel()

	logBefore := b3ToolLogLines(used)
	memBefore := agent.currentMemory.Size()
	delegBefore := state.DelegationAttempts
	_, repoWritesBefore := repo.snapshot()

	res, err := runner.Call(ctx, used, sc.args, "call_"+sc.id)

	wroteLog := b3ToolLogLines(used) > logBefore
	memDelta := agent.currentMemory.Size() - memBefore
	delegDelta := state.DelegationAttempts - delegBefore
	_, repoWritesAfter := repo.snapshot()
	repoSet := repoWritesAfter > repoWritesBefore

	b3AppendLogPhase(rec, wroteLog)
	b3AppendDelegatePost(rec, used, &memDelta, &delegDelta, &repoSet)
	rec.append("ret:" + b3ResultShape(res))
	rec.append("err:" + b3ErrLabel(err))
	b3TimeoutElements(rec, sc, res, err)
	if mon != nil && sc.behavior != b3Block {
		rec.append("heartbeat:" + mon.awaitKind(t))
	}

	return b3SideRun{tool: used, seq: rec.snapshot(),
		result: res, execErr: err, found: true, invoked: rec.invoke.Load()}
}

type b3ScenarioRun struct {
	sc b3Scenario
	a  b3SideRun
	b  b3SideRun
}

func b3RunMatrix(t *testing.T) []b3ScenarioRun {
	t.Helper()
	return b3RunIDs(t)
}

func b3RunIDs(t *testing.T, ids ...string) []b3ScenarioRun {
	t.Helper()
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	runs := make([]b3ScenarioRun, 0, len(b3Scenarios))
	for _, sc := range b3Scenarios {
		if len(want) > 0 && !want[sc.id] {
			continue
		}
		var a, b b3SideRun
		if !t.Run(sc.id, func(st *testing.T) {
			a, b = b3RunExecutorSide(st, sc), b3RunDirectorSide(st, sc)
		}) {
			t.Errorf("场景 %q 的子用例没产出序列，后续判定不含它", sc.id)
			continue
		}
		runs = append(runs, b3ScenarioRun{sc: sc, a: a, b: b})
	}
	return runs
}

func b3KeyOf(elem string) string {
	if i := strings.Index(elem, ":"); i >= 0 {
		return elem[:i]
	}
	return elem
}

func b3ValOf(elem string) string {
	if i := strings.Index(elem, ":"); i >= 0 {
		return elem[i+1:]
	}
	return elem
}

const b3Absent = "-"

func b3ElementsByKey(seq []string) map[string]string {
	out := make(map[string]string, len(seq))
	for _, e := range seq {
		k, v := b3KeyOf(e), b3ValOf(e)
		if prev, ok := out[k]; ok {
			out[k] = prev + "+" + v
			continue
		}
		out[k] = v
	}
	return out
}

type b3Diff struct {
	Scenario string
	Key      string
	A        string
	B        string
}

func (d b3Diff) String() string {
	return fmt.Sprintf("%s / %s: A=%q B=%q", d.Scenario, d.Key, d.A, d.B)
}

func b3DiffsOf(runs []b3ScenarioRun) []b3Diff {
	var out []b3Diff
	for _, r := range runs {
		ga, gb := b3ElementsByKey(r.a.seq), b3ElementsByKey(r.b.seq)
		seen := make(map[string]bool, len(ga)+len(gb))
		keys := make([]string, 0, len(ga)+len(gb))
		for k := range ga {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
		for k := range gb {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			va, vb := b3Absent, b3Absent
			if v, ok := ga[k]; ok {
				va = v
			}
			if v, ok := gb[k]; ok {
				vb = v
			}
			if va == vb {
				continue
			}
			out = append(out, b3Diff{Scenario: r.sc.id, Key: k, A: va, B: vb})
		}
	}
	return out
}

type expectedDiff struct {
	Scenario string
	Key      string
	A        string
	B        string
	Reason   string
	Repro    string
}

func (e expectedDiff) matches(d b3Diff) bool {
	if e.Key != d.Key {
		return false
	}
	if e.Scenario != "*" && e.Scenario != d.Scenario {
		return false
	}
	if e.A != "*" && e.A != d.A {
		return false
	}
	if e.B != "*" && e.B != d.B {
		return false
	}
	return true
}

var wantDiffs = []expectedDiff{
	{
		Scenario: "*", Key: "event", A: "tool_call_start", B: b3Absent,
		Reason: "层次不同，不是漏发：A 在 runToolCall 内部发（executor.go:134），B 的发点在上一层的 " +
			"planner（planner_delegate.go:86，紧邻 Tools.Call）。本契约直接驱动 runner.Call，" +
			"所以 B 侧看不见它。合并时必须二选一：把发布点上提到与 A 同一层，或把 A 的下移到循环层 —— " +
			"两边都留着就是同一个事件发两次（TUI 会画两条同样的工具行）。",
		Repro: "任何走到事件发布的场景；cold-tool 最干净（两侧都没有执行相位，只剩这一条差异）。",
	},
	{
		Scenario: "*", Key: "carrier", A: "no", B: "yes",
		Reason: "B 每次调用都无条件挂 outcome 载体（director.go:861 withDelegationOutcome），" +
			"子 Agent 侧没有这一步：它的委派结果由 delegateTo 直接写便签与交付，不经载体回传。",
		Repro: "任何进入 Adapter.Call 的场景（readonly 最简）。载体不在委派分支里，所以普通只读工具也能看见。",
	},
	{
		Scenario: "*", Key: "log", A: "wrote", B: "none",
		Reason: "A 收尾写一条工具日志（executor.go:173 logToolCall → tool_logger.go），B 完全不写。" +
			"两侧的执行日志因此不对称：Director 的一次工具调用只留在 rollout/journal 与 delegate-*.log 里，" +
			"tool-*.log 只有子 Agent 的。",
		Repro: "readonly；agent-exit 用真 agent_exit 也能复现（与探针无关，是执行层的固定收尾相位）。",
	},
	{
		Scenario: "*", Key: "deadline", A: "180s", B: "120s",
		Reason: "同一个\"工具级超时\"两套配置：A 用 var defaultToolTimeout=180s（executor.go:95,161），" +
			"B 用 var directorToolTimeout=120s（director.go:786，读一次于 :845、套用于 :875）—— " +
			"B4' 把 B 那个函数内字面量提升成了包级 var（为的是对称 seam，**数值刻意没统一**）。" +
			"这正是蓝图 d22f 记的那处漂移：改任一处都要同时核对 PlannerConfig.ToolTimeout" +
			"（planner_delegate.go:107 只用于错误文案，且因为下一条 sentinel 差异它其实够不着），" +
			"否则回执里的秒数与真实 deadline 不符。",
		Repro: "readonly / tool-error / hb-*（凡是非委派、非交互、且真执行了的场景）。" +
			"timeout-expiry 不贡献这条：那里两侧都被 seam 缩到同一个亚秒标签。",
	},
	{
		Scenario: "timeout-expiry", Key: "timeout", A: "text", B: "error",
		Reason: "*/ret 那条（格式化位置差一层）在超时到期路径上的实证：A 在 executor.go:166-168 " +
			"就地写成 \"Error: tool execution timed out after N seconds\" 并**同时**交回 execErr；" +
			"B 在 director.go:901-902 交回空串 + 一条含同样句子的 error，模型看到的 \"Error: …\" 是" +
			"上一层 planner_delegate.go:109 补的。两侧都给模型一句超时话，产出它的位置差一层 —— " +
			"分层差异，不是漏写；合并时二选一（都在执行层格式化，或都不在）。",
		Repro: "timeout-expiry（两侧 seam 都缩到 b3ToolTimeoutSeam，b3Hang 探针挂到到期）。",
	},
	{
		Scenario: "timeout-expiry", Key: "sentinel", A: "deadline", B: "rewritten",
		Reason: "A 交回的 execErr 就是原始的 context.DeadlineExceeded（executor.go:175 原样返回），" +
			"所以上层 errors.Is(err, context.DeadlineExceeded) 认得出这是超时；B 在 director.go:902 用" +
			"不带 %w 的 fmt.Errorf 把它换成一条纯文案 ⇒ 哨兵丢失，于是 planner_delegate.go:106 那条" +
			"专门处理 DeadlineExceeded 的分支（以及 PlannerConfig.ToolTimeout 这个配置）对 B 自己的" +
			"工具级超时永远够不着，只能落到通用的 \"Error: %s\"。两侧给模型的文本因此同形（见上一条），" +
			"差别只在分类能力。⇒ 疑似遗漏而非分层必然：合并内核时要么补 %w，要么删掉那条死分支，" +
			"别把它照搬成第三份。本次不改生产，只把现状钉住。",
		Repro: "timeout-expiry。B 侧把 director.go:902 改成 %w 包裹本错误，标签立刻变 deadline、" +
			"这条条目随即腐烂变红 —— 那是期望的信号，不是回归。",
	},
	{
		Scenario: "delegate", Key: "deadline", A: "180s", B: "none",
		Reason: "委派那一档 B 不套 WithTimeout，而是 activity.WithIdleTimeout（director.go:870）—— " +
			"没有 deadline 是**设计**：心跳能续 idle，续不了 total。A 侧对所有非交互工具一律套死线，" +
			"于是子 Agent 里一次 delegate_* 既没有看门狗也没有 idle 感知，只有 180s 硬上限。",
		Repro: "delegate。",
	},
	{
		Scenario: "*", Key: "ret", A: "error-text", B: "empty",
		Reason: "执行失败时 A 就地格式化回执（executor.go:170 的 \"Error: %v\"）并**同时**交回 execErr；" +
			"B 交回空串 + error，把格式化交给上一层（planner_delegate.go:109）。" +
			"两侧都给模型的最终文本是 \"Error: …\"，但产出它的位置差一层。",
		Repro: "tool-error（普通失败）与 hb-at-exec（失败原因是监视器取消）—— 后一侧的文案还带 idle 分类，见文件头。",
	},
	{
		Scenario: "unknown-name", Key: "ret", A: "empty", B: "notfound",
		Reason: "名字对上但这个 Agent 没有该工具：A 交回空串并把 found=false 交给调用方去合成回执" +
			"（executor.go:143），B 就地给模型一句 \"Tool x not found\"（director.go:819）。" +
			"两侧 err 都是 nil —— 这是既有口径，路标注释在 TestToolExecContractKnownSemantics。",
		Repro: "unknown-name。",
	},
	{
		Scenario: "delegate", Key: "delegid", A: "none", B: "injected",
		Reason: "委派坐标（tool_call_id）只在 B 注入 ctx（director.go:869 memory.WithDelegationID）；" +
			"A 只读不写（executor.go:236 取已有坐标绑发布器）。生产里子 Agent 看到的坐标正是上游 B 写进去的，" +
			"本场景两侧共用一条干净 ctx，所以这条差异表达的是\"谁负责注入\"，不是\"谁少了坐标\"。",
		Repro: "delegate。",
	},
	{
		Scenario: "delegate", Key: "pool", A: "none", B: "held",
		Reason: "per-Run 委派池只存在于 Director 侧（director.go:852 acquireDelegateSlot；AGENTS.md 硬约束 10）。" +
			"子 Agent 侧的 delegate_* 由 toolspec 标 Serial、经 toolbatch 串行执行，不另建池 —— " +
			"嵌套委派因此用的是自己那一轮的池，\"父占一格、子在同一格里排队\"的形状从结构上不存在。",
		Repro: "delegate。序列只表达\"调用期间占着一格\"，不表达释放配对（见文件头的不覆盖清单）。",
	},
	{
		Scenario: "delegate", Key: "meminject", A: "absent", B: "+1",
		Reason: "B 把子 Agent 的摘要注入自己的 ConversationMemory（director.go:884 injectSubAgentMemory）。" +
			"A 侧没有这个槽位：子 Agent 的会话历史就是它自己的 messages，由循环落账。",
		Repro: "delegate（探针适配器像真 delegate 闭包那样调 recordDelegationOutcome 才有这一步）。",
	},
	{
		Scenario: "delegate", Key: "deleg", A: "absent", B: "+1",
		Reason: "RecordDelegation（director.go:889）是 Director 的 HasDelegated/强制委派提醒那台状态机的账，" +
			"子 Agent 没有对应物。",
		Repro: "delegate。",
	},
	{
		Scenario: "delegate", Key: "repoctx", A: "absent", B: "set",
		Reason: "delegate_repo 的摘要回填 repoCtx（director.go:907）只属于 Director —— " +
			"它要在下一轮把仓库蓝图注进 system prompt；子 Agent 侧没有这个消费者。",
		Repro: "delegate。",
	},
	{
		Scenario: "hb-at-exec", Key: "heartbeat", A: "tool:start:b3_probe_hb_block", B: "start",
		Reason: "A 在执行前后各发一次 activity 心跳（executor.go:131 起 / :174 止），B 一次都不发：" +
			"\"start\" 是监视器创建时写下的哨兵标签，读到它就等于这条链上一次心跳都没进过。" +
			" Director 侧不需要它（委派档有自己的 idle 看门狗），但子 Agent 侧的心跳是 delegate_idle_timeout " +
			"判据的唯一续期来源 —— 别把它当成噪声删掉。",
		Repro: "hb-at-exec（阻塞式探针在执行期间读数）。",
	},
	{
		Scenario: "hb-after-done", Key: "heartbeat", A: "tool:done:b3_probe_hb_done", B: "start",
		Reason: "同上，读的是收尾之后槽位里的最后一次活动：A 是 tool:done:*，B 仍是创建时的哨兵。",
		Repro:  "hb-after-done（快返回的探针在调用结束后读数）。",
	},
}

func b3EqualSeq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestToolExecutionContract(t *testing.T) {
	for _, r := range b3RunMatrix(t) {
		if len(r.sc.wantExecutor) == 0 || len(r.sc.wantDirector) == 0 {
			t.Errorf("场景 %q 的 golden 还没定：A=%v B=%v", r.sc.id, r.a.seq, r.b.seq)
			continue
		}
		if !b3EqualSeq(r.a.seq, r.sc.wantExecutor) {
			t.Errorf("场景 %q 子 Agent 侧序列漂移:\n got=%v\nwant=%v", r.sc.id, r.a.seq, r.sc.wantExecutor)
		}
		if !b3EqualSeq(r.b.seq, r.sc.wantDirector) {
			t.Errorf("场景 %q Director 侧序列漂移:\n got=%v\nwant=%v", r.sc.id, r.b.seq, r.sc.wantDirector)
		}
		if r.a.tool != r.b.tool {
			t.Errorf("场景 %q 两侧调用的工具名不同（夹具失配，差异不可解释）: A=%q B=%q", r.sc.id, r.a.tool, r.b.tool)
		}
	}
}

func TestToolExecDiffAllowlistFresh(t *testing.T) {
	for i, e := range wantDiffs {
		if e.Scenario == "" || e.Key == "" || e.A == "" || e.B == "" {
			t.Errorf("白名单条目 #%d 字段不全: %+v", i, e)
		}
		if strings.TrimSpace(e.Reason) == "" {
			t.Errorf("白名单条目 #%d (%s/%s) 缺 Reason：不可解释的条目等于给漂移开了后门", i, e.Scenario, e.Key)
		}
		if strings.TrimSpace(e.Repro) == "" {
			t.Errorf("白名单条目 #%d (%s/%s) 缺 Repro：没法复现的条目没人会去核对", i, e.Scenario, e.Key)
		}
	}
	for i := range wantDiffs {
		for j := i + 1; j < len(wantDiffs); j++ {
			if wantDiffs[i] == wantDiffs[j] {
				t.Errorf("白名单条目 #%d 与 #%d 完全重复：%v", i, j, wantDiffs[i])
			}
		}
	}

	runs := b3RunMatrix(t)
	diffs := b3DiffsOf(runs)

	t.Logf("实测差异 %d 条，白名单 %d 条（两侧各 %d 个观测场景）", len(diffs), len(wantDiffs), len(runs))
	for _, d := range diffs {
		t.Logf("  实测 → %s", d)
	}
	if len(diffs) == 0 {
		t.Errorf("两侧序列没有任何差异，但白名单有 %d 条 —— 条目全部腐烂（本次没复现）。", len(wantDiffs))
	}
	matched := make([]bool, len(wantDiffs))

	for _, d := range diffs {
		hit := -1
		for i, e := range wantDiffs {
			if e.matches(d) {
				hit = i
				break
			}
		}
		if hit < 0 {
			t.Errorf("未列入白名单的差异 → %s", d)
			continue
		}
		matched[hit] = true
	}
	for i, e := range wantDiffs {
		if !matched[i] {
			t.Errorf("白名单条目本次未复现（腐烂）→ #%d %s/%s A=%q B=%q reason=%s",
				i, e.Scenario, e.Key, e.A, e.B, e.Reason)
		}
	}
}

func TestToolExecContractKnownSemantics(t *testing.T) {
	runs := b3RunIDs(t, "unknown-name", "cold-tool")
	byID := make(map[string]b3ScenarioRun, len(runs))
	for _, r := range runs {
		byID[r.sc.id] = r
	}

	r := byID["unknown-name"]
	if r.a.execErr != nil {
		t.Errorf("未知工具：A 侧 execErr = %v, want nil（既有口径）", r.a.execErr)
	}
	if r.b.execErr != nil {
		t.Errorf("未知工具：B 侧 err = %v, want nil（既有口径）", r.b.execErr)
	}
	if r.a.found {
		t.Errorf("未知工具：A 侧 found = true, want false —— Surface 是唯一工具来源，查不到就是没有")
	}
	if !strings.HasSuffix(r.b.result, "not found") {
		t.Errorf("未知工具：B 侧回执 = %q, want 一句 'Tool x not found'（它必须给模型一句话，而不是空串）", r.b.result)
	}

	c := byID["cold-tool"]
	if !strings.HasPrefix(c.a.result, "Error: schema for ") || !strings.HasPrefix(c.b.result, "Error: schema for ") {
		t.Errorf("冷工具：两侧必须都交回 teach 回执 A=%q B=%q", firstChars(c.a.result), firstChars(c.b.result))
	}
	if c.a.result != c.b.result {
		t.Errorf("冷工具的 teach 文案已经分叉（两侧同一条文案有两份实现）:\nA=%s\nB=%s",
			firstChars(c.a.result), firstChars(c.b.result))
	}
	if c.a.execErr != nil || c.b.execErr != nil {
		t.Errorf("冷工具：teach 不算执行失败，两侧 err 都应为 nil，实得 A=%v B=%v", c.a.execErr, c.b.execErr)
	}
}

func TestToolExecTimeoutExpiryReceipt(t *testing.T) {
	runs := b3RunIDs(t, "timeout-expiry")
	if len(runs) != 1 {
		t.Fatalf("timeout-expiry 场景没跑起来：%d 条", len(runs))
	}
	r := runs[0]

	if r.a.execErr == nil {
		t.Errorf("A 侧超时到期后 execErr = nil, want 非 nil（原始 context.DeadlineExceeded）")
	}
	if r.b.execErr == nil {
		t.Errorf("B 侧超时到期后 err = nil, want 非 nil")
	}

	if !strings.Contains(r.a.result, "timed out") {
		t.Errorf("A 侧超时语义没进模型可见回执：%q", firstChars(r.a.result))
	}
	if r.b.result != "" {
		t.Errorf("B 侧失败时应当交回空串（格式化留给上一层），实得 %q", firstChars(r.b.result))
	}
	if r.b.execErr == nil || !strings.Contains(r.b.execErr.Error(), "timed out") {
		t.Errorf("B 侧超时语义没进 error：%v", r.b.execErr)
	}

	if !errors.Is(r.a.execErr, context.DeadlineExceeded) {
		t.Errorf("A 侧应当原样交回 context.DeadlineExceeded，实得 %v", r.a.execErr)
	}
	if errors.Is(r.b.execErr, context.DeadlineExceeded) {
		t.Errorf("B 侧的超时 error 现在**不带** DeadlineExceeded 哨兵（白名单 sentinel 条目）；" +
			"这条突然成立说明有人给生产补了 %%w —— 请同步删掉白名单那条并改掉本断言")
	}

	t.Logf("timeout-expiry 实测：A result=%q err=%v | B result=%q err=%v",
		firstChars(r.a.result), r.a.execErr, firstChars(r.b.result), r.b.execErr)
}

func firstChars(s string) string {
	const keep = 120
	if len(s) <= keep {
		return s
	}
	return s[:keep] + "...[truncated]"
}

func TestToolExecContractUsesProductionEntrypoints(t *testing.T) {
	runs := b3RunIDs(t, "readonly", "delegate")
	if len(runs) != 2 {
		t.Fatalf("场景子集没跑齐：%d, want 2", len(runs))
	}
	var aInvoke, bInvoke, aEvents int64
	aLogWrote, bCarrierYes, bDelegCounted := 0, 0, 0
	for _, r := range runs {
		aInvoke += r.a.invoked
		bInvoke += r.b.invoked
		aEvents += int64(r.a.events)
		aLogWrote += b3CountElem(r.a.seq, "log:wrote")
		bCarrierYes += b3CountElem(r.b.seq, "carrier:yes")
		bDelegCounted += b3CountElem(r.b.seq, "deleg:+1")
	}
	if aInvoke == 0 || bInvoke == 0 {
		t.Errorf("探针适配器执行计数 A=%d B=%d, want 两侧都 >0（有一条没进 Adapter.Call）", aInvoke, bInvoke)
	}
	if aEvents == 0 {
		t.Errorf("A 侧没收到 tool_call_start：runToolCall 根本没被调用，契约测的是别的东西")
	}
	if aLogWrote == 0 {
		t.Errorf("A 侧一条工具日志都没落：logToolCall 没跑到，说明执行的不是 runToolCall")
	}
	if bCarrierYes == 0 {
		t.Errorf("B 侧没挂上 outcome 载体：Call 里 withDelegationOutcome 那一段没执行")
	}
	if bDelegCounted == 0 {
		t.Errorf("B 侧委派计数没变：Call 里 RecordDelegation 那一段没执行")
	}
}

func b3CountElem(seq []string, elem string) int {
	n := 0
	for _, e := range seq {
		if e == elem {
			n++
		}
	}
	return n
}
