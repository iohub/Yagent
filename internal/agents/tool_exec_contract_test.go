package agents

// B3 —— 两份工具执行实现的契约测试 + 差异白名单 + 新鲜度闸门（零生产代码改动）。
//
// 被测的两个生产入口（同处 package agents，所以不需要任何自造 seam）：
//   - A 子 Agent 侧：runToolCall(ctx, cfg *ExecutorConfig, tc llm.ToolCall)（executor.go）
//   - B Director 侧：directorToolRunner.Call(ctx, name, argsJSON, callID)（director.go），
//     由唯一构造方式 newDirectorToolRunner 建出来
//
// 本文件把同一组输入场景分别喂给 A 与 B，各自产出一条**按真实发生顺序**的观测序列，
// 然后做三件事：
//   1. TestToolExecutionContract        —— 两侧的序列各自等于 golden 序列（顺序也被钉住）；
//   2. TestToolExecDiffAllowlistFresh    —— A/B 序列的差异必须**逐条**落在白名单里，
//      且白名单条目本次必须真的复现（腐烂也红）；
//   3. TestToolExecContractUsesProductionEntrypoints —— 两侧的"生产入口指纹"计数 >0，
//      防的是"契约测试绿了但跑的是自造路径"（根 AGENTS.md §11 坑 33：ExecutorConfig.Adapters
//      那份测试专用快照导致生产并行一次都没发生而测试全绿）。
//
// 观测面从哪来（除下面点名的一处超时 seam 外，全部是既有导出/包内实体，没有为测试
// 新增的钩子；seam 只是把生产已有的两个 deadline 值改成可写的，不新增分支）：
//   - 事件：复用本包既有替身 spyPublisher（executor_parallel_test.go），外面只加一层"把
//     Publish 记进同一条序列"的转发，不另造第二个事件假具；
//   - 工具执行：真实 tools.Surface 装配（NewSurfaceFromAdapters / 生产构造函数 surfaceFor），
//     探针适配器只挂在面上，执行仍走 tools.Adapter.Call；
//   - ctx 相位：在探针适配器内部读 ctx.Deadline()/memory.DelegationIDFrom/本包的
//     delegationOutcomeKey —— 这些是生产写进 ctx 的东西，测试只在最下游读，不写。
//
// 观测面的三条限制（承认限制比假装覆盖更有用）：
//   - 心跳只有一个读数通道：activity 包不导出"读最后活动标签"的 API，Heartbeat 只写原子槽位。
//     唯一能把标签交出来的地方是监视器因空闲取消时的 *activity.IdleTimeoutError.LastActivityKind，
//     而它是**最后一次**活动的快照 —— 所以 hb-at-exec / hb-after-done 两条合起来才钉住
//     "执行前有 start、收尾后有 done"这一整对；其余场景里没有 heartbeat 元素（读了也只是重复读数）。
//   - 序列里放的是**形态与标签**（ret:text / deadline:120s / carrier:yes），不是原文：
//     原文含工具名与 schema，白名单条目会随注册表漂移而腐烂。逐字等值只在"两侧现在确实相同"
//     的那一处比（冷工具的 teach 文案，见 TestToolExecContractKnownSemantics）。
//   - 后置相位按**工具名**决定是否记录，不按"这是哪一侧"：否则夹具自己就在制造差异。
//
// B4' 补的超时 seam（本文件唯一的测试专用生产改动，两处都是"把已有的值改成可写"）：
//   - A 侧 defaultToolTimeout（executor.go:95）本来就是 var，helper 是既有的
//     restoreToolTimeout（executor_test.go:61）；
//   - B 侧原来是 director.go 里那个函数内字面量 120s，B4' 提升成包级 var
//     directorToolTimeout（director.go:786），本文件的 restoreDirectorToolTimeout 与之对称。
//     ⇒ "超时真的响起之后两侧各交回什么"第一次进了对称契约：见 timeout-expiry 场景。
//
// 明确**不覆盖**的维度（不要假装覆盖了）：
//   - 超时回执里的**秒数**与整句原文：timeout-expiry 把两侧 seam 缩到毫秒级，文案会诚实地
//     写"after 0 seconds"（int(60ms/time.Second) 与 int((60ms).Seconds()) 都是 0）。那是
//     seam 的形状不是生产的规范，所以这里只断言"包含 timed out 这一可判别特征"+ 落在哪个
//     通道（timeout: 元素）。生产那两个秒数由 deadline 元素与白名单的 180s/120s 条目记账。
//   - 超时**之后上一层怎么分类**：委派档的 IdleTimeoutError / TotalTimeoutError 两支只有 B 有
//     （A 侧没有对应档），无法在同一场景里对称触发；由 e2e_lifecycle_test.go 的端到端用例钉，
//     这里不重复付那份 1s 采样延迟的耗时。timeout-expiry 比的是**非委派档**那条共同的 deadline。
//   - 冷工具 teach 回执在 Materialize 失败那一支：B3 论证它在生产不可达（StateColdKnown
//     要求 hidden() 为真，而 hidden() 在 opts.Deferred 关闭时恒 false，见 surface.go:306-308
//     与 :336-338），B4' 复核成立后**已删掉 B 那一份**（director.go:821-836）⇒ 两侧冷工具
//     路径同形，这一维不再是"差异"，也不再需要构造。A 侧 teachColdTool（executor.go:224-230）
//     的那条错路文案仍留着，合并内核时二选一。
//   - 并行分批（toolbatch.Run/Plan 的游程分组与提交顺序）、暂停闸门、rollout 落账：
//     都在调用方的循环里，不在被测的两个入口内。分别由 executor_parallel_test.go、
//     e2e_pause_test.go、executor_stopkind_test.go 钉。
//   - 委派池的**记账**（acquire/release 配对、取不到格子时交回 ctx.Err()、急停开关）：
//     序列只能表达"调用期间是否占着一格"，不能表达"释放回去了没有"。后者由
//     delegate_concurrency_test.go 钉。
//   - agent_exit 的**终止判定**本身（found/err 之上还有一层 exitpolicy）：不在这两个入口里，
//     由 termination_gate_test.go 与 executor_agent_exit_test.go 钉。这里只钉到
//     "执行层把一次成功的 agent_exit 交回成 err=nil 的普通回执"（agent-exit 那条序列）。

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

// ─── 观测面：一条按发生顺序追加的序列 ────────────────────────────────────────

// b3Rec 是跨观测源共用的顺序记录器。事件总线、探针适配器、驱动侧的后置相位都往同一份
// 记录里追加，所以"事件先于执行、执行先于落日志"这类先后关系是真序，不是事后拼出来的。
type b3Rec struct {
	mu     sync.Mutex
	seq    []string
	invoke atomic.Int64 // 探针适配器真正被执行（进了 tools.Adapter.Call 内部）的次数
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

// b3Bus 只在本包既有替身 spyPublisher 前面加一层顺序记录：事件类型进序列，内容留在 spy 里。
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

// ─── 受控工具：真实 Surface + 探针适配器 ─────────────────────────────────────

type b3Behavior string

const (
	b3OK      b3Behavior = "ok"      // 正常返回
	b3Fail    b3Behavior = "fail"    // 返回 error（模拟执行失败）
	b3Outcome b3Behavior = "outcome" // 回填委派结果载体：真 delegate 闭包做的事
	b3Block   b3Behavior = "block"   // 阻塞到 ctx 结束（心跳观测用）
	b3Hang    b3Behavior = "hang"    // 阻塞到**工具级 deadline** 响起（超时到期观测用）
)

// b3Probe 是两侧**共用**的适配器工厂：同一个构造函数、同一个闭包体，
// 只有 pool（Director 侧才有委派池）与 rec 不同。两侧观测到的差异因此只能来自
// 被测代码，不可能来自夹具。
type b3Probe struct {
	rec      *b3Rec
	behavior b3Behavior
	pool     chan struct{} // nil = 子 Agent 侧（那一侧没有 per-Run 委派池）
}

// observeCall 是探针适配器进场的固定观测：调用、deadline、ctx 载体、委派坐标，
// 委派名字再多一项池子占用。两侧走的是**同一个方法**，所以四项观测的取值差异
// 只可能来自被测代码。
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

// adapterFunc 把真实/模拟的工具函数包进观测：执行的仍是传进来的实现，
// tools.Adapter.Call 那一段（参数解析、guard、结果序列化）一个字都没被替掉。
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
			// 心跳探针：监视器响之后 LastActivityKind 就定稿了，在这儿把它读回来。
			// 10s 是纯保险（监视器每 b3CheckInterval 采样一次，正常必然先响），
			// 真撞上就说明观测通道本身失效了 —— 让 no-idle-cause 标签把失败喊出来。
			select {
			case <-ctx.Done():
			case <-time.After(10 * time.Second):
				return nil, errors.New("b3: activity monitor never fired")
			}
			p.rec.append("heartbeat:" + b3KindOfCause(ctx))
			return nil, ctx.Err()
		case b3Hang:
			// 超时到期探针：挂到**执行 ctx** 结束为止，把 ctx 的错原样交回 —— 真工具卡死时
			// 就是这个形状。它与 b3Block 的区别只有一条：这里不读心跳标签（场景没套 activity
			// 监视器，读了只会得到"no-idle-cause"这种与超时无关的噪声）。
			// 6s 是纯保险：正常路径上 seam 只有几十毫秒，保险丝真撞上就说明**那一侧的工具级
			// deadline 根本没套上**（变异验证关心的正是这件事），于是下面那条错文案会把失败喊出来。
			select {
			case <-ctx.Done():
			case <-time.After(6 * time.Second):
				return nil, errors.New("b3: tool-level deadline never fired")
			}
			return nil, ctx.Err()
		case b3Outcome:
			// 真 delegate 闭包做的事（director_surface.go:172 recordDelegationOutcome）。
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

// productionExitAdapter 用注册表里的真 agent_exit（FlowControlTool.ExecuteAgentExit），
// 外面只套一层观测 —— 收尾判据在 exitpolicy，不在执行层，这条序列要能证明这点。
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
		// 亚秒级 deadline 只可能来自超时 seam（timeout-expiry 场景把两侧都缩到几十毫秒）。
		// 那里钉数字就是钉 flaky：探针读到余量的那一刻与建 ctx 之间隔多少由调度决定，
		// 舍入到秒会在 0s/-1s 之间翻脸，而两侧各读各的 —— 折成同一个标签，
		// "两侧确实都套了工具级 deadline"这件事一分不减。秒级以上照旧报数字（180s/120s）。
		return "subsecond"
	}
	return fmt.Sprintf("%ds", int(rem.Round(time.Second)/time.Second))
}

// b3HasOutcomeCarrier 读的是本包 directorToolRunner.Call 写进 ctx 的那个载体。
// 测试与生产同包，所以这个判据不需要新增导出面。
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

// b3ResultShape 把回执文本折成形态标签。序列里放形态而不是原文：原文含工具名与 schema，
// 会让白名单条目随注册表漂移而腐烂，而"两侧给模型看到的是同一类东西"才是契约。
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

// b3TimeoutSemantics 把"超时这句话到底交给了谁"折成标签，钉的是**可判别特征**而不是全文：
// 两侧生产文案都必须含 "timed out"（A 在 executor.go:168 就地写进回执，
// B 在 director.go:902 把它塞进 error、回执留空给上一层去格式化），但落在哪个通道不同。
// 不比对句中的秒数：那个数字来自 seam（两侧都是几十毫秒 ⇒ int 折成 0），
// 钉它等于把测试自己缩短的超时写进 golden。
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

// b3DeadlineSentinel 钉的是**上一层还能不能凭 errors.Is 认出这是一次超时**：
// A 把原始 execErr（context.DeadlineExceeded）原样交回上层（executor.go:175 的第三个返回值），B 在
// director.go:902 换成一条不带 %w 的新 error ⇒ 哨兵丢在这里。
// 这条观测是白名单里 sentinel 那一条的证据，也是 B4 合并内核时必须当场决定的那件事。
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

// restoreDirectorToolTimeout 是 B 侧的超时 seam，与 executor_test.go:61 的
// restoreToolTimeout 逐字对称：临时改短 directorToolTimeout（director.go:786），
// 恢复交给 t.Cleanup。没有它，"超时到期之后交回什么"这条生产行为在 Director 侧
// 永远只能靠真等 120 秒来观察 —— B3 之前正是这个缺口让那一维完全没人测。
func restoreDirectorToolTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := directorToolTimeout
	directorToolTimeout = d
	t.Cleanup(func() { directorToolTimeout = old })
}

// b3ToolTimeoutSeam 是两侧对称缩短到的工具级超时：远小于任何真实工具的正常耗时，
// 又远大于 0（探针进 Adapter.Call 只需要微秒级）。配合 b3Hang 的 6s 保险丝，
// 它不响就是测试失败，而不是把套件挂住。
const b3ToolTimeoutSeam = 60 * time.Millisecond

// b3TimeoutElements 把超时到期那两个专属观测写进序列。**两侧调用同一段**、按场景的
// behavior 决定是否记录（不按"这是哪一侧"）—— 与 heartbeat/pool 那两条规则同形，
// 否则差异就是夹具自己造的。
func b3TimeoutElements(rec *b3Rec, sc b3Scenario, result string, err error) {
	if sc.behavior != b3Hang {
		return
	}
	rec.append("timeout:" + b3TimeoutSemantics(result, err))
	rec.append("sentinel:" + b3DeadlineSentinel(err))
}

// b3ToolLogLines 数工具日志里属于"本契约两侧身份 + 这个工具"的行数。A 侧的 logToolCall 走的是
// ~/.yagent/logs/tool-*.log（测试二进制的 $HOME 由 TestMain 指到临时目录），B 侧不写这条 ——
// 它是"后置相位"里唯一落到文件系统的观测点。
//
// needle 必须带 agent 身份：本包其他用例（含并行用例）也会写同名工具的行（agent_exit、
// delegate_repo…），只按工具名数会把别人的日志算成自己的，于是 log:none 被翻成 log:wrote、
// 白名单跟着失真。两个身份都数：A 用 cfg.AgentName，B 若将来补上日志只会用 director 那个名字。
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

// b3RepoCtx 实现生产端口 RepoContextStore（NewDirectorAgent 也从 params 注入同一个接口）。
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

// ─── 心跳监视器（activity 包没有"读最后心跳"的导出 API，用取消原因读它）────────

const (
	// 被驱动的调用是纯内存操作（<5ms），100ms 的空闲窗口给到 20 倍余量：
	// 监视器只可能在调用**之后**响，不会在半路把在飞的调用杀掉。
	b3IdleTimeout   = 100 * time.Millisecond
	b3CheckInterval = 2 * time.Millisecond
)

type b3Monitor struct {
	ctx context.Context
}

// awaitKind 等监视器因空闲取消，并取回它记录的最后活动标签。
// 这是 activity 包唯一能把"有没有人发过心跳"交出来的通道：Heartbeat 只写原子槽位，
// 包外（含本包测试）没有任何读接口。
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

// ─── 场景表 ──────────────────────────────────────────────────────────────────

// b3PickCold 表示"工具名在运行期从生产 deferred 面上取第一个冷工具"。
// 两侧各自构造同一份面（同参数、同注册表），所以取到的名字必然相同 —— 这条由
// TestToolExecutionContract 显式核对，不靠假设。
const b3PickCold = "\x00pick-cold"

type b3Scenario struct {
	id        string
	tool      string
	args      string
	behavior  b3Behavior
	deferred  bool // true = 用生产构造函数 surfaceFor 装配的 deferred 面
	monitored bool // true = ctx 套 activity 监视器（心跳探针）
	realExit  bool // true = 面上的 agent_exit 用注册表里的真实现
	callOnly  bool // true = 面上故意不放 sc.tool（"名字对上但没注册"那条口径）

	// 两侧各自的期望序列（golden）。顺序也算断言，所以"事件在调用之前"这类事实被钉在这里，
	// 而白名单只管元素取值差异 —— 两道闸门各守一种漂移：golden 防"两侧一起漂走"，
	// 白名单防"只有一边漂走"。留空 = 该场景还没定契约，TestToolExecutionContract 直接红。
	wantExecutor []string
	wantDirector []string
}

// b3Scenarios 是本契约的输入矩阵。两侧跑**同一张表**，各自产序列。
//
// golden 里的 deadline:180s / deadline:120s 记的是两侧**现在各用的秒数**（executor.go:95 的
// defaultToolTimeout 与 director.go:786 的 directorToolTimeout，两侧都是 var、都有 seam），
// 不是"本该如此"的规范。B4 要把它们统一的话，两侧 golden 连同白名单的 deadline 条目必须
// 一起改 —— 让"统一超时"这件事在测试里显式发生，而不是悄悄发生。
// timeout-expiry 那条是例外：它两侧都被 seam 缩到同一个亚秒标签（deadline:subsecond），
// 钉的是"两侧都真套上了工具级 deadline"，不是秒数。
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
		// 超时到期（B4' 补的对称场景）：两侧用**各自的 seam** 把工具级 deadline 缩到
		// b3ToolTimeoutSeam，驱动一个挂到到期才返回的探针，比"到期之后交回什么"。
		// 两条专属观测按 behavior 记录（两侧同一段代码）：
		//   timeout:  超时这句话落在哪个通道 —— A 写进模型可见回执文本，B 只写进 error；
		//   sentinel: 上一层还能不能用 errors.Is(err, DeadlineExceeded) 认出是超时 ——
		//             A 交回原始哨兵，B 在 director.go:902 换成裸 fmt.Errorf 把哨兵抹掉了。
		// 不断言句中的秒数（那个 0 是 seam 折出来的，见文件头的不覆盖清单）。
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
		// 名字对上但没注册：两侧都交回 nil error，这是**既有口径**而不是 bug ——
		// 见 TestToolExecContractKnownSemantics 里的路标注释。
		id:       "unknown-name",
		tool:     "b3_probe_unregistered",
		args:     "{}",
		behavior: b3OK,
		callOnly: true,
		// 两侧都不进 Adapter.Call（A 在 resolveAdapter 就返回，B 在 resolveTool 就返回），
		// 所以这一条序列里没有执行相位 —— 只有回执形态不同。
		wantExecutor: []string{"event:tool_call_start", "log:none", "ret:empty", "err:none"},
		wantDirector: []string{"log:none", "ret:notfound", "err:none"},
	},
	{
		// 注册表里的真 agent_exit：两侧都只是"又执行了一次工具"、都交回 nil error。
		// 序列里没有任何终止相位 —— 那正是 B2（终止判据单源化）之后的形状：收尾判定
		// 在 exitpolicy，不在执行层。
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
		// 冷工具：两侧交回**逐字相同**的 teach 回执（等值性由
		// TestToolExecContractKnownSemantics 直接比字符串钉住 —— 那条文案现在有两份实现）。
		id:           "cold-tool",
		tool:         b3PickCold,
		args:         "{}",
		deferred:     true,
		wantExecutor: []string{"event:tool_call_start", "log:none", "ret:teach-cold", "err:none"},
		wantDirector: []string{"log:none", "ret:teach-cold", "err:none"},
	},
	{
		// 交互工具：两侧都按注册表（toolspec.IsInteractive）豁免工具级超时，deadline 同取
		// none ⇒ **这一条两侧一致**，不进白名单。判据只有一份的先例就该长这样。
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
		// 委派：两侧差异最集中的一条 —— 池子、委派坐标、摘要注入、委派计数、repoCtx。
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
		// 心跳探针（执行期间读数）：适配器阻塞到 activity 监视器因空闲取消，再把监视器
		// 记下的"最后活动标签"读回来。A 读到 tool:start:… 证明 start 心跳先于执行落槽；
		// B 读到的是监视器创建时的哨兵 "start" —— 这条链上一次心跳都没进过。
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
		// 心跳探针（收尾之后读数）：A 的 tool:done 心跳是本次调用留在槽位里的最后一次活动。
		// 元素排在 ret/err 之后是真的观测位置（只能等监视器响），不是生产顺序被改写。
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

// ─── 场景装配与两侧驱动 ──────────────────────────────────────────────────────

func b3ToolCall(id, name, args string) llm.ToolCall {
	return llm.ToolCall{
		ID:       id,
		Type:     "function",
		Function: llm.FunctionCall{Name: name, Arguments: args},
	}
}

// b3FirstColdToolName 取面上第一个冷工具（toolspec.Names 已排序，所以选谁与 map 序无关）。
func b3FirstColdToolName(s *tools.Surface) string {
	for _, n := range toolspec.Names() {
		if _, st := s.Lookup(n); st == tools.StateColdKnown {
			return n
		}
	}
	return ""
}

// b3BuildSurface 按场景装配工具面，返回面与实际调用的工具名。
// pool 只用于探针适配器观测"调用期间是否占着委派池的一格"，不参与装配。
//
// 两侧各自调用本函数（同参数）：冷工具场景的 teach 分支会 Materialize 并改写面的状态，
// 共用一份面的话后跑的一侧看到的就是热工具了 —— 那不是差异，是夹具自己造的假象。
func b3BuildSurface(t *testing.T, sc b3Scenario, probe b3Probe) (*tools.Surface, string) {
	t.Helper()
	if sc.deferred {
		// 生产构造函数 + 画像齐备 deps + 全局 deferred 配置：Director 每轮 rebuildSurface
		// 走的就是这一条，冷工具因此是真的冷而不是测试编出来的状态。
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
		// 注册表里的真 agent_exit（流程控制工具），两侧同一个实现。
		adapters = []*tools.Adapter{probe.productionExitAdapter(t)}
	case sc.callOnly:
		// 面上有别的工具，只缺被调用的那一个 —— "这个 Agent 有工具面，但这个名字不在面上"
		// 才是生产的形状（空面会让两侧都走另一条早退路，测不到判据）。
		adapters = []*tools.Adapter{probe.adapter("b3_probe_sibling", "sibling tool that is on the surface")}
	default:
		adapters = []*tools.Adapter{probe.adapter(sc.tool, "b3 contract probe tool")}
	}
	return tools.NewSurfaceFromAdapters("b3-probe", adapters...), sc.tool
}

// b3SideRun 是一次驱动的完整观测：序列 + 原始返回（供逐场景补充断言用）。
type b3SideRun struct {
	tool    string // 实际调用的工具名（冷工具场景在运行期才定）
	seq     []string
	result  string
	execErr error
	found   bool // 只有 A 侧签名里有；B 侧不表达"找没找到"
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

// b3AppendDelegatePost 只按**工具名**决定是否记录（两侧同一规则），不按"这是哪一侧"：
// 这一侧有没有 RunState/摘要注入槽位是事实，不是夹具的分支。
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

// b3RunExecutorSide 驱动 A：runToolCall。
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

	// 超时到期场景：用 A 侧**既有**的 seam 缩短工具级 deadline（B 侧对称的那条见
	// b3RunDirectorSide）。两侧都在自己的驱动函数里改自己的包级 var，
	// 数值同源（b3ToolTimeoutSeam）⇒ 序列里的差异只可能来自被测代码。
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
		// 非阻塞行为下没有"执行期间"的观测点，等监视器把最后活动定稿后再读 ——
		// 它读到的是本次调用留下的最后一次心跳。
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

// b3RunDirectorSide 驱动 B：newDirectorToolRunner(...).Call。
func b3RunDirectorSide(t *testing.T, sc b3Scenario) b3SideRun {
	t.Helper()
	rec := &b3Rec{}
	repo := &b3RepoCtx{}
	agent := &DirectorAgent{
		repoCtx:                  repo,
		currentMemory:            memory.NewConversationMemory(64),
		delegateIdleTimeout:      30 * time.Second, // >0：委派分支的 WithIdleTimeout 契约要求（0 会 panic）
		maxParallelReadOnlyTools: NormalizeMaxParallelReadOnlyTools(4),
		maxParallelDelegations:   2,
	}
	state := &director.RunState{MaxSteps: 4}
	runner := newDirectorToolRunner(agent, state)

	// 超时到期场景：B4' 给 Director 侧补的 seam（与 A 侧 restoreToolTimeout 对称）。
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

// b3RunMatrix 跑完整张场景表。
func b3RunMatrix(t *testing.T) []b3ScenarioRun {
	t.Helper()
	return b3RunIDs(t)
}

// b3RunIDs 跑指定 id 的子集（不传 = 全表）。便宜场景单独跑，别让指纹用例
// 为一根心跳探针付 200ms。
//
// 每个场景各占一条**子用例**，这不是为了输出好看，是为了 seam 的作用域：
// 两侧的超时 helper（restoreToolTimeout / restoreDirectorToolTimeout）都把还原挂在
// t.Cleanup 上，而 t.Cleanup 的粒度是"整条用例"。挂在父用例上，timeout-expiry 一跑,
// 后面每条场景的 deadline golden 都会跟着变短 —— 夹具泄漏会被读成"生产漂了"。
// 不并行（包级 var 的写必须串行），与本包既有的超时用例同一个约定。
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
			// 子用例里 Fatal 掉了（夹具本身失配），本次矩阵就少一条场景的观测。
			// 红已经红了，这里补一句是为了别让人把"少跑一条"读成"白名单腐烂"。
			t.Errorf("场景 %q 的子用例没产出序列，后续判定不含它", sc.id)
			continue
		}
		runs = append(runs, b3ScenarioRun{sc: sc, a: a, b: b})
	}
	return runs
}

// ─── 差异引擎 ─────────────────────────────────────────────────────────────────

// b3KeyOf/b3ValOf 把元素拆成 "key:value"。元素词表固定为
// event/call/deadline/carrier/delegid/pool/heartbeat/log/meminject/deleg/repoctx/ret/err/
// timeout/sentinel（后两个只在 b3Hang 那条场景里出现，见 b3TimeoutElements）。
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

// b3ElementsByKey 把序列折成 key→取值。同一 key 出现多次时按出现顺序用 '+' 连接，
// 于是"某侧多发布了一个事件"表达成取值差异，而不是靠位置碰运气。
//
// 顺序信息不在这里守 —— 它由每个场景的 golden 序列守住（见 TestToolExecutionContract）；
// 本函数只负责把"两侧各自多/少了什么"算干净，避免 A 侧多一个元素导致整条序列错位。
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

// b3Diff 是单个场景里单个观测面的两侧差异。
type b3Diff struct {
	Scenario string
	Key      string
	A        string // 子 Agent 侧取值；b3Absent = 这一侧没有这个元素
	B        string // Director 侧取值
}

func (d b3Diff) String() string {
	return fmt.Sprintf("%s / %s: A=%q B=%q", d.Scenario, d.Key, d.A, d.B)
}

// b3DiffsOf 把矩阵折成差异清单。两侧都没这个 key 时不产生条目（不算差异）。
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

// ─── 差异白名单 ───────────────────────────────────────────────────────────────

// expectedDiff 是一条**已核对**的结构差异。它必须说清两件事：
// 为什么这是分层造成的结构差异（而不是某一侧漏写），以及在哪条场景能复现它。
// Reason/Repro 任一为空 = 白名单条目本身不合格，闸门直接红。
type expectedDiff struct {
	Scenario string // 场景 id；"*" = 任何场景
	Key      string // 观测面 key
	A        string // A 侧取值；"*" = 任意；b3Absent = A 侧没有该元素
	B        string // B 侧取值
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

// wantDiffs 是白名单本体，逐条按**实测序列**填写（不是按设计稿的表格）。
//
// 设计稿那张 7 维度对照表里，有两条在本契约的观测面下**不成立**，因此没有对应条目：
//   - "冷工具处理不一致"：两侧交回的是逐字相同的 teach 回执，err 都是 nil
//     （executor.go:224 teachColdTool 与 director.go:835 是同一条文案的两份实现 ——
//     等值性由 TestToolExecContractKnownSemantics 直接比字符串钉住，B4 改一处即红）；
//   - "交互工具超时豁免不一致"：两侧都问同一个注册表（toolspec.IsInteractive），
//     deadline 同取 none，序列里根本不构成差异。
//
// 判定口径：白名单里逐条说明差在哪、为什么差（见各条 Reason）。前 14 条 + 本次新增的
// timeout 那条都是**分层造成的结构差异**（哪一侧拥有某个槽位/事件/日志），没有一条是
// "某一侧漏写" —— 疑似遗漏本来不该进白名单，它必须被修掉。
//
// 唯一的例外按名字点名：`timeout-expiry / sentinel` 那条记的是 B 侧超时 error 丢了
// DeadlineExceeded 哨兵，那是**实测到的疑似遗漏**，本次按任务纪律不许为了让测试好写而
// 改生产代码，所以先如实入账并把"合并内核时补 %w 还是删死分支"写进 Reason。
// 后来人别把它当成结构性差异照搬进合并后的内核。
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
		// 这条**不是**分层必然结果，而是实测到的疑似遗漏：按仓库规矩本该修掉再入账，
		// 但本次任务明确禁止为了让测试好写而改生产代码，所以先如实记账、把决定权交给 B4。
		// 措辞里点明这一点，别让后来人把它当成"结构性差异"照原样搬进合并后的内核。
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

// ─── 用例 ─────────────────────────────────────────────────────────────────────

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

// TestToolExecutionContract 钉住两侧各自的有序序列（golden）。
// 这道闸门管"顺序与元素集合"：两侧一起漂走也会被它抓到，白名单抓不到。
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

// TestToolExecDiffAllowlistFresh 是双向闸门：
//   - 没列入白名单的实际差异 → 红（"再漂移就被发现"）；
//   - 列入但本次没复现的条目 → 红（有人把差异消灭了却留着条目，白名单就会慢慢变成谎话）；
//   - 条目自身缺 Reason/Repro、或重复 → 红（不可解释的条目等于没有条目）。
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
	// -v 时把实测差异全量交出来：白名单说"两侧差在哪"，这条说"现在实测还差在这些"。
	// 核对/新增条目时先看这份清单，别对着代码猜。
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

// TestToolExecContractKnownSemantics 钉住三条"不能靠序列形状表达、但必须留下痕迹"的口径。
func TestToolExecContractKnownSemantics(t *testing.T) {
	runs := b3RunIDs(t, "unknown-name", "cold-tool")
	byID := make(map[string]b3ScenarioRun, len(runs))
	for _, r := range runs {
		byID[r.sc.id] = r
	}

	// 1) 未注册的工具名。
	//
	// ⚠️ 路标（不是 bug，别顺手"修好"它）：两侧对"名字对上但这个 Agent 没有该工具"都交回
	// **nil error** —— A 是 ("", found=false, nil)，B 是 ("Tool x not found", nil)。
	// 于是"没注册"在两条循环里都**不算执行失败**：终止判据（exitpolicy）不会因为它把
	// 一次 agent_exit 判成失败，模型看到的是一条普通回执。想改成"报错/算失败"，
	// 必须先改下面这三条断言，再同时改两个调用方对 found/err 的用法 —— 只改一边就是
	// 又一份"两边各写一份判据"的漂移（根 AGENTS.md §11 坑 33 同形）。
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

	// 2) 冷工具：两侧交回的 teach 文本必须**逐字相同**。
	//
	// 它是本契约里唯一一处"两侧现在一致、但实现是两份"的地方（executor.go 的 teachColdTool
	// 与 director.go 的内联分支各写了一遍同一条文案）。B4 合并时只要改其中一处，
	// 这条就会红 —— 这正是留着它的理由。
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

// TestToolExecTimeoutExpiryReceipt 是超时到期那一维的**内容**断言（序列里放的是标签，
// 标签背后的原文在这里比）：两侧都必须真的把"这是一次超时"交给上层，只是交法不同。
// B4' 之前这一维一条测试都没有（B 侧没有 seam，等一次超时要付 120 秒）。
func TestToolExecTimeoutExpiryReceipt(t *testing.T) {
	runs := b3RunIDs(t, "timeout-expiry")
	if len(runs) != 1 {
		t.Fatalf("timeout-expiry 场景没跑起来：%d 条", len(runs))
	}
	r := runs[0]

	// 两侧都算执行失败：err 非 nil。这一条两侧**一致**，所以不进白名单 ——
	// 谁哪天把一侧改成 nil（超时就不算失败了），这里立刻红。
	if r.a.execErr == nil {
		t.Errorf("A 侧超时到期后 execErr = nil, want 非 nil（原始 context.DeadlineExceeded）")
	}
	if r.b.execErr == nil {
		t.Errorf("B 侧超时到期后 err = nil, want 非 nil")
	}

	// 可判别特征：句子里必须有 "timed out"。全文不比（含 seam 折出来的秒数），
	// 落点不同这一事实由 timeout: 元素 + 白名单那条记账。
	if !strings.Contains(r.a.result, "timed out") {
		t.Errorf("A 侧超时语义没进模型可见回执：%q", firstChars(r.a.result))
	}
	if r.b.result != "" {
		t.Errorf("B 侧失败时应当交回空串（格式化留给上一层），实得 %q", firstChars(r.b.result))
	}
	if r.b.execErr == nil || !strings.Contains(r.b.execErr.Error(), "timed out") {
		t.Errorf("B 侧超时语义没进 error：%v", r.b.execErr)
	}

	// 哨兵：A 保得住，B 抹掉了。这里**如实钉现状**（不是期望值）：
	// 哪天有人给 director.go:902 补上 %w，这条会红 —— 那时该做的是删掉白名单里
	// sentinel 那条并同步这条断言，而不是把它改回容忍状。
	if !errors.Is(r.a.execErr, context.DeadlineExceeded) {
		t.Errorf("A 侧应当原样交回 context.DeadlineExceeded，实得 %v", r.a.execErr)
	}
	if errors.Is(r.b.execErr, context.DeadlineExceeded) {
		t.Errorf("B 侧的超时 error 现在**不带** DeadlineExceeded 哨兵（白名单 sentinel 条目）；" +
			"这条突然成立说明有人给生产补了 %%w —— 请同步删掉白名单那条并改掉本断言")
	}

	// 交出来的原文只为了核对，不参与判定（-v 才看得见）。
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

// TestToolExecContractUsesProductionEntrypoints 防的是"契约测试绿了、测的却是自造路径"：
// 两侧的序列里必须出现**只有那个生产入口才会留下**的指纹，且计数必须 >0。
//
//	A 侧指纹：tool_call_start 事件（只有 runToolCall 发布）+ 工具日志行（只有它的 logToolCall 写）
//	B 侧指纹：outcome 载体进 ctx（只有 directorToolRunner.Call 挂）+ RecordDelegation 计数
//	两侧共同：探针适配器被真正执行（说明走的是 tools.Adapter.Call，不是手搓 fake adapter）
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
