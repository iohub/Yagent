package blueprint

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// 本文件是设计稿 §5.1 的结构层持有者，也是 §14 并发表里的三行：
// 派生 goroutine 是**唯一写者**（整份快照构造完后 atomic.Pointer 一次替换），
// 注入与 read_blueprint 只 Load()（不加锁、不阻塞、不等派生，I15），
// 落盘钩子只写文档、不碰快照。
//
// 快照必须是不变值，否则 atomic.Pointer 只是把数据竞争换成可见性问题 ——
// 这条由 scan_types.go 顶部"所有字段只在 Scan 装配时赋值"的纪律保证。

// 渲染预算的兜底常数（§10：配置只有 5 个键，其余是常数）。
// config 的 [blueprint] 默认值与这两个数**必须相等**，由
// internal/config/blueprint_defaults_test.go 钉住（坑 18：同形两份只有一边会漂走）。
const (
	DefaultIndexMaxTokens    = 1000
	DefaultProseBudgetTokens = 1500
)

// DeriveBudget 是 §5.1 的成本自适应分界：扫一次低于它就**每轮重派生**（无缓存、
// 无失效判据、无一致性问题——没有缓存就少一整类 bug），高于它才启用
// "缓存 + 仓库读数变化才失效"。
//
// 这条推翻**有到期条件**：一旦某个仓库扫描超过 150ms（5 万文件量级），
// 缓存 + sha 锚定增量刷新重新变成正解。写下到期条件是为了让实施者
// 不把推翻当信念，也不要偷偷加回来而不说触发条件。
const DeriveBudget = 150 * time.Millisecond

// deriveBudget 是上面那条常量的测试 seam（与 datamanager.taskFlushDebounce、
// agents.stepRetryBackoffBase 同族）：模式判据不能用"真扫一个慢仓库"来测 ——
// 那会把 150ms 算进测试时长，还是测不出"只看第一次取样"这条。
var deriveBudget = DeriveBudget

// DeriveSpec 是派生层的输入。
type DeriveSpec struct {
	ProjectPath string
	Scan        Options

	// RepoToken 是一个"仓库动了就会变"的廉价读数：git HEAD 的 sha；
	// 非 git 仓库由调用方退化成项目根 mtime（§5.1）。只在缓存模式下被调用。
	//
	// 做成参数而不是 import gitinfo：本包是 leaf 包，而"怎么读到 HEAD"是装配层的事
	// （gitinfo 会 fork 子进程，不该由一个数据层包决定）。返回 "" 按"读数缺失"处理，
	// 缓存模式下等于不重扫 —— 所以装配层必须给一个真有退化值的实现（I9）。
	RepoToken func() string

	// ScanFn 是扫描入口的 seam，默认取 Scan。
	// 存在的理由只有一个：成本自适应（DeriveBudget 分界）不能用真扫一次慢仓库来测，
	// 那会把 150ms 变成测试时长，也会让"模式判定只看第一次取样"这条断言无从下笔。
	ScanFn func(projectPath string, opts Options) (*Snapshot, error)
}

// Status 是派生层的账目，供 read_blueprint 的 header 与 `/blueprint` 回答
// "这一轮为什么没有索引"。
type Status struct {
	Ready      bool
	Scanning   bool
	CachedMode bool          // false = 每轮重派生（默认形态）
	Cost       time.Duration // 最近一次扫描耗时
	Head       string        // 最近一次扫描时的 RepoToken
	Modules    int
	LastErr    string
	// Requests/Skips 是缓存模式的账目：请求了几次、其中几次因为"仓库没动"被跳过。
	// 没有这两个数，"仓库没变时不重扫"就只能靠 sleep 之后数一数，那正是永远绿不过的测试。
	Requests int
	Skips    int
}

// Deriver 持有当前快照。零值不可用，必须经 NewDeriver。
type Deriver struct {
	spec DeriveSpec

	snap atomic.Pointer[Snapshot]

	mu     sync.Mutex
	status Status
	// firstSample 记下"成本模式已经取样过没有"。它放在 Deriver 上而不是 Status 上：
	// Status 是给调用方打印的账目，不是一个可以倒回去改的状态机。
	firstSample bool

	// wake 容量 1：Request 非阻塞发送，所以"扫描期间到达的多次请求"自然合并成
	// 一次后续扫描，不需要额外的 pending 标志位。
	wake   chan struct{}
	quit   chan struct{}
	once   sync.Once
	closed atomic.Bool
}

// NewDeriver 建一个还没起跑的派生层。
func NewDeriver(spec DeriveSpec) *Deriver {
	if spec.RepoToken == nil {
		spec.RepoToken = func() string { return "" }
	}
	if spec.ScanFn == nil {
		spec.ScanFn = Scan
	}
	return &Deriver{
		spec: spec,
		wake: make(chan struct{}, 1),
		quit: make(chan struct{}),
	}
}

// Run 是 §14 的唯一写者循环：**阻塞**，由调用方决定它跑在哪条 goroutine 上
// （生产走 supervise.From(ctx).Go，测试里可以直接 DeriveOnce 或不起线程）。
// ctx 取消或 Close 即退出，返回时不留任何后台工作 —— `--prompt` 那条一次性路径
// 全靠这一点才能干净收尾。
func (d *Deriver) Run(ctx context.Context) {
	// 起跑那一次也走同一条失效判据：没就绪时 needsRescan 恒真，
	// 而已经就绪（例如装配层先同步派生过一次）时不许无条件重扫 ——
	// 两条入口各有各的判据就是第二份事实，而那正是"同一轮扫两遍"的来源。
	if d.needsRescan() {
		d.derive(ctx)
	}
	for {
		select {
		case <-ctx.Done():
			d.setScanning(false)
			return
		case <-d.quit:
			d.setScanning(false)
			return
		case <-d.wake:
			d.countRequest()
			if !d.needsRescan() {
				d.countSkip()
				continue
			}
			d.derive(ctx)
		}
	}
}

// DeriveOnce 同步跑一次派生（CLI `yagent blueprint`、测试、以及"注入侧发现还没就绪
// 且这是第一次"的兜底）。与 Run 共用同一份存储路径，所以两者可以并存而不产生第二写者：
// 快照是不可变值，替换的只是指针。
func (d *Deriver) DeriveOnce(ctx context.Context) error {
	return d.derive(ctx)
}

// Request 请求"看一眼仓库有没有动"。永不阻塞、永不失败（I15）。
func (d *Deriver) Request() {
	if d.closed.Load() {
		return
	}
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// Snapshot 返回当前快照；还没扫完时返回 nil（调用方据此渲染 ScanningLine）。
func (d *Deriver) Snapshot() *Snapshot { return d.snap.Load() }

// CurrentStatus 返回账目快照。
func (d *Deriver) CurrentStatus() Status {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.status
}

// Close 让 Run 退出并停止接受新请求。重复调用安全（sync.Once）。
// （"等它真退了"由 supervise 的 Drain 负责，本包不再造第二个 WaitGroup。）
func (d *Deriver) Close() {
	d.closed.Store(true)
	d.once.Do(func() { close(d.quit) })
}

func (d *Deriver) countRequest() {
	d.mu.Lock()
	d.status.Requests++
	d.mu.Unlock()
}

func (d *Deriver) countSkip() {
	d.mu.Lock()
	d.status.Skips++
	d.mu.Unlock()
}

func (d *Deriver) derive(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.setScanning(true)

	start := time.Now()
	snap, err := d.spec.ScanFn(d.spec.ProjectPath, d.spec.Scan)
	cost := time.Since(start)
	if err != nil {
		// 扫描失败**不清空旧快照**：一次 git 读数失败或临时 IO 错误
		// 不该让整个索引消失（§5.2"指纹不符不删除"是同一条理由）。
		d.mu.Lock()
		d.status.LastErr = err.Error()
		d.status.Scanning = false
		d.mu.Unlock()
		return err
	}

	head := d.spec.RepoToken()
	d.snap.Store(snap)

	d.mu.Lock()
	// 模式只在**第一次取样**时定：之后来回翻转会让"要不要缓存"随两次扫描的抖动变化，
	// 而那正是最难复现的一类失效（§5.1 的到期条件说的是仓库形状，不是单次抖动）。
	if !d.firstSample {
		d.status.CachedMode = cost >= deriveBudget
		d.firstSample = true
	}
	d.status.Ready = true
	d.status.Scanning = false
	d.status.Cost = cost
	d.status.Head = head
	d.status.Modules = len(snap.Leaves)
	d.status.LastErr = ""
	d.mu.Unlock()
	return nil
}

// needsRescan 是缓存模式的失效判据：读数变了才重扫。
// cheap 模式恒为真 —— 那才是"没有缓存就没有失效判据"的形态。
func (d *Deriver) needsRescan() bool {
	d.mu.Lock()
	cached, head, ready := d.status.CachedMode, d.status.Head, d.status.Ready
	d.mu.Unlock()
	if !cached || !ready {
		return true
	}
	return d.spec.RepoToken() != head
}

func (d *Deriver) setScanning(v bool) {
	d.mu.Lock()
	d.status.Scanning = v
	d.mu.Unlock()
}
