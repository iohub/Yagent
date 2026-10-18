// Package supervise 给进程里每一条 goroutine 一个名字、一次 recover、一个等它的人。
//
// 存在理由（docs/structured-concurrency-design.md D1）：这 24 处裸 go 里 12 处没有 recover
// （其中两处会调 LLM，panic 直接崩进程），而且**没有一处能被等待** —— 退出时"还在跑"是常态
// 而不是异常。errgroup 给不出"到底是哪一条卡住"的答案，所以这里自己写：成员必须具名，
// Drain 超时返回的是名字清单，不是一句 timeout。
//
// 依赖方向：只准标准库（同 internal/pause、internal/blueprint 的叶子性，守卫见
// scripts/check_supervise_leaf.sh）。
package supervise

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

type ctxKey struct{}

// Lease 是一条还没退出的 goroutine 的快照。
type Lease struct {
	Name      string // Go 时给的名字（可读，允许重复）
	Key       string // 域内唯一句柄（name#序号）
	Owner     string // 所属域的名字链，从 root 开始
	StartedAt time.Time
	Age       time.Duration
}

func (l Lease) String() string {
	return fmt.Sprintf("%s@%s(%s)", l.Name, l.Owner, l.Age.Round(time.Millisecond))
}

// LeaseNames 把泄漏清单压成适合进 slog 字段的一串名字。
// 只在包里写一份：调用点各抄一份 loop 就是 §11 坑 18 说的"两份同形实现只有一边会漂走"。
func LeaseNames(leaked []Lease) []string {
	out := make([]string, 0, len(leaked))
	for _, l := range leaked {
		out = append(out, l.String())
	}
	return out
}

type entry struct {
	name    string
	key     string
	started time.Time
}

// Group 是一条并发域：一个进程、一次任务、一次委派。
type Group struct {
	name   string
	parent *Group
	ctx    context.Context

	// 等待协议：waiter 在锁内"看一眼 live 并领一只 channel"，release 在锁内"若归零则关闭并
	// 摘走这只 channel"。两边都在同一把锁下操作，所以既不会漏唤醒（waiter 领到的 channel
	// 若已被关闭，下一次循环就看见 live==0），也不会攒下陈旧信号。
	// 用这套而不是 sync.WaitGroup，是因为 Add-after-Wait 在本仓是合法并发 ——
	// 工具批会在一次 Drain 进行中新起 goroutine，而 sync.WaitGroup 明确禁止那种时序。
	quiet chan struct{}

	mu       sync.Mutex
	live     map[string]entry
	subs     map[*Group]bool
	seq      int
	failures map[string]int
}

var (
	rootOnce  sync.Once
	rootGroup *Group
)

// Root 是进程级域：没有 ctx、或 ctx 上没有域时的落脚点。
func Root() *Group {
	rootOnce.Do(func() { rootGroup = newGroup("root", nil, context.Background()) })
	return rootGroup
}

// New 建一条子域，父域从 ctx 读（读不到就挂 Root）。
//
// 必须用返回的 ctx 继续往下传：只攥着 *Group 而不用新 ctx，下游 From(ctx) 仍会拿到父域，
// 等于白建 —— 域的归属靠 ctx，不靠调用点自觉传指针。
func New(ctx context.Context, name string) (*Group, context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	g := newGroup(name, From(ctx), ctx)
	g.parent.mu.Lock()
	g.parent.subs[g] = true
	g.parent.mu.Unlock()
	return g, context.WithValue(ctx, ctxKey{}, g)
}

// From 取回 ctx 所属的域；没有则返回 Root。永不 nil，且所有方法对 nil receiver 安全
// （照 internal/pause 的先例：测试与 --prompt 路径一行都不用改）。
func From(ctx context.Context) *Group {
	if ctx == nil {
		return Root()
	}
	if g, ok := ctx.Value(ctxKey{}).(*Group); ok && g != nil {
		return g
	}
	return Root()
}

// WithGroup 把域塞进 ctx（供已经持有 *Group 的调用点使用，例如跨结构体字段重建 ctx）。
func WithGroup(ctx context.Context, g *Group) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if g == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, g)
}

// Go 起一条具名 goroutine。
//
// fn 拿到的是域 ctx 本身，**不是**派生的 cancel ctx：本包不制造取消（AGENTS.md §11 坑 17
// 的教训是"取消会被误判成一次失败的委派"），fn 必须自己尊重 ctx 与自己的超时。
// panic 在这里被 recover 并计入 Failures —— 不记就等于静默丢掉一份后台工作。
func (g *Group) Go(name string, fn func(ctx context.Context)) {
	if g == nil {
		Root().Go(name, fn)
		return
	}
	if fn == nil {
		panic("supervise: Go called with nil fn")
	}
	if name == "" {
		name = "unnamed"
	}

	g.mu.Lock()
	g.seq++
	e := entry{name: name, key: fmt.Sprintf("%s#%d", name, g.seq), started: time.Now()}
	g.live[e.key] = e
	g.mu.Unlock()

	go func() {
		defer g.release(e.key)
		defer func() {
			if r := recover(); r != nil {
				g.recordPanic(name, r)
			}
		}()
		fn(g.ctx)
	}()
}

// Drain 等本域及其全部子域的成员退出，最长 timeout。返回还没退出的成员清单。
// 它不取消任何东西，也不是错误信号 —— 拿到空切片就是排干了。
func (g *Group) Drain(timeout time.Duration) []Lease {
	if g == nil {
		return nil
	}
	return g.drainAt(time.Now().Add(timeout))
}

// Pending 不等待，只报告此刻还在跑的成员（含子域）。
func (g *Group) Pending() []Lease {
	if g == nil {
		return nil
	}
	return g.pending(time.Now())
}

// Failures 返回本域及其子域里被 recover 掉的 panic 次数，按名字聚合。
func (g *Group) Failures() map[string]int {
	out := map[string]int{}
	g.collectFailures(out)
	return out
}

// Context 是域 ctx（fn 收到的就是它）。
func (g *Group) Context() context.Context {
	if g == nil {
		return context.Background()
	}
	return g.ctx
}

// Name 是建域时给的名字。
func (g *Group) Name() string {
	if g == nil {
		return ""
	}
	return g.name
}

func newGroup(name string, parent *Group, ctx context.Context) *Group {
	return &Group{
		name:   name,
		parent: parent,
		ctx:    ctx,
		live:   map[string]entry{},
		subs:   map[*Group]bool{},
	}
}

func (g *Group) drainAt(deadline time.Time) []Lease {
	// 先等子域：子域里的 goroutine 也是"本域还没结束的工作"。
	for {
		sub := g.anySub()
		if sub == nil || !time.Now().Before(deadline) {
			break
		}
		if len(sub.drainAt(deadline)) > 0 {
			break // 子域没排干，再转圈也只是烧预算
		}
	}
	for {
		g.mu.Lock()
		n := len(g.live)
		var wake chan struct{}
		if n > 0 {
			if g.quiet == nil {
				g.quiet = make(chan struct{})
			}
			wake = g.quiet
		}
		g.mu.Unlock()

		if n == 0 || !time.Now().Before(deadline) {
			break
		}
		select {
		case <-wake:
		case <-time.After(time.Until(deadline)):
		}
	}
	leaks := g.pending(deadline)
	if len(leaks) == 0 && g.parent != nil {
		g.parent.forget(g) // 排干就摘掉自己，否则父域永远看得见一条空域
	}
	return leaks
}

func (g *Group) release(key string) {
	g.mu.Lock()
	delete(g.live, key)
	g.armQuietLocked()
	g.mu.Unlock()
}

func (g *Group) forget(sub *Group) {
	g.mu.Lock()
	delete(g.subs, sub)
	g.armQuietLocked()
	g.mu.Unlock()
}

// armQuietLocked 在域刚好安静下来时唤醒所有等待者。调用者必须持锁。
func (g *Group) armQuietLocked() {
	if g.quiet != nil && len(g.live) == 0 && len(g.subs) == 0 {
		close(g.quiet)
		g.quiet = nil
	}
}

func (g *Group) recordPanic(name string, r any) {
	g.mu.Lock()
	if g.failures == nil {
		g.failures = map[string]int{}
	}
	g.failures[name]++
	g.mu.Unlock()
	slog.Error("supervised goroutine panicked",
		"domain", g.path(), "goroutine", name, "panic", r,
		"stack", string(debug.Stack()))
}

func (g *Group) anySub() *Group {
	g.mu.Lock()
	defer g.mu.Unlock()
	for sub := range g.subs {
		return sub
	}
	return nil
}

func (g *Group) pending(now time.Time) []Lease {
	g.mu.Lock()
	lives := make([]entry, 0, len(g.live))
	for _, e := range g.live {
		lives = append(lives, e)
	}
	subs := make([]*Group, 0, len(g.subs))
	for sub := range g.subs {
		subs = append(subs, sub)
	}
	g.mu.Unlock()

	out := make([]Lease, 0, len(lives)+len(subs))
	owner := g.path()
	for _, e := range lives {
		started := e.started
		out = append(out, Lease{
			Name: e.name, Key: e.key, Owner: owner,
			StartedAt: started, Age: now.Sub(started),
		})
	}
	for _, sub := range subs {
		out = append(out, sub.pending(now)...)
	}
	return out
}

func (g *Group) collectFailures(out map[string]int) {
	if g == nil {
		return
	}
	g.mu.Lock()
	for name, n := range g.failures {
		out[name] += n
	}
	subs := make([]*Group, 0, len(g.subs))
	for sub := range g.subs {
		subs = append(subs, sub)
	}
	g.mu.Unlock()
	for _, sub := range subs {
		sub.collectFailures(out)
	}
}

// path 是 Root 到本域的名字链，泄漏报告里用它回答"这条卡在哪次委派上"。
func (g *Group) path() string {
	var names []string
	for cur := g; cur != nil; cur = cur.parent {
		names = append(names, cur.name)
	}
	for i, j := 0, len(names)-1; i < j; i, j = i+1, j-1 {
		names[i], names[j] = names[j], names[i]
	}
	return strings.Join(names, "/")
}
