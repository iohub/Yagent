package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yagent/internal/messaging"
)

// 一个 WorkspaceGuard 是「一次 Init 一个实例、任务内全部 Agent 共享」
// （internal/app/app.go:314 构造 → 六个 params.Guard → tools/surface.go:158 挂到
// 每个 adapter）。Director 一步里并行 delegate_repo/delegate_chat
// （agents/specs.go:71,73 是唯二 mutates:false 的委派工具）时，两个子 Agent 的
// goroutine 会同时命中 Check 的读与 RequestAuth 的写；没有 mu 的话这不是"偶发
// 错乱"而是 runtime throw（fatal error: concurrent map read and map write），
// recover 拦不住（agents/toolbatch 的 guardedExec 也拦不住）。
//
// 用例形状照抄同包先例 design_test.go:51：**去掉锁才红，且主要在 -race 下红**
// （scripts/check_all.sh 的 RACE_PKGS 含 internal/tools）。所以这条用例的价值必须
// 靠 `go test -race` 跑出来，非 race 构建里它只是"没崩"。

// guardAuthProbe 一条"没被任何 bypass 放行时必然要求授权"的工具调用样本。
// 四个探针覆盖 Check 的三条分派分支（shell / fs_write 单路径 / fs_write 路径数组 /
// browser_eval），于是 RequestAuth 写 sessionAllowed 的键不止一个：多键并发插入
// 才是那个 throw 的形状。
type guardAuthProbe struct {
	tool   string
	params map[string]interface{}
}

// guardResponder 是测试里的"用户"：接住每一次 user_help_needed，按 request_id 用
// confirm.OnUserResponse 回给定答复。接线形状照抄 internal/agents/e2e_authorization_test.go:129。
//
// 不能用 nil confirmMgr 走捷径：那样 RequestAuth 在守卫的 nil 判定处就返回 nil，
// 写点一次都到不了，用例退化成"只测了读"。
type guardResponder struct {
	confirm *UserConfirmManager
	answer  atomic.Pointer[string]
	asked   atomic.Int64 // 真的弹给过用户几次
}

func (r *guardResponder) ID() string { return "guard-test-responder" }

func (r *guardResponder) Types() []messaging.EventType {
	return []messaging.EventType{messaging.EventType("user_help_needed")}
}

func (r *guardResponder) Consume(_ context.Context, ev *messaging.Event) error {
	content, ok := ev.Content.(map[string]interface{})
	if !ok {
		return nil
	}
	r.asked.Add(1)
	requestID, _ := content["request_id"].(string)
	if requestID == "" {
		return nil
	}
	if ans := r.answer.Load(); ans != nil {
		r.confirm.OnUserResponse(requestID, *ans)
	}
	return nil
}

// guardAuthHarness 复刻 app.go 的装配顺序：dispatcher → publisher →
// confirm.SetPublisher → NewWorkspaceGuard(workDir, confirm) → RegisterConsumer(confirm)
// → Subscribe(responder)。SetPublisher 漏了的话 RequestConfirmation 直接报
// "publisher not set"，用例看到的只是"从没问过用户"。
type guardAuthHarness struct {
	dispatcher *messaging.MessageDispatcher
	publisher  *messaging.MessagePublisher
	confirm    *UserConfirmManager
	responder  *guardResponder
}

func newGuardAuthHarness(t *testing.T) *guardAuthHarness {
	t.Helper()

	dispatcher := messaging.NewMessageDispatcher(256)
	publisher := messaging.NewMessagePublisher(dispatcher)
	confirm := NewUserConfirmManager()
	confirm.SetPublisher(publisher)

	h := &guardAuthHarness{
		dispatcher: dispatcher,
		publisher:  publisher,
		confirm:    confirm,
		responder:  &guardResponder{confirm: confirm},
	}
	// 关键事件旁路（message_dispatcher.go:32 criticalEventTypes 含 user_help_needed）
	// 保证授权请求不被满载丢弃，答复一定到得了。
	dispatcher.RegisterConsumer(confirm)
	dispatcher.Subscribe(h.responder)

	t.Cleanup(dispatcher.Shutdown)
	return h
}

const (
	guardWaveSpinners = 16
	guardWaveIters    = 50
)

// runGuardWave 在同一个 guard 上让"读侧"（Check）与"写侧"（RequestAuth 的三个分支
// + SetYoloMode）真的撞上。每波用**新建的 guard**：sessionAllowed 回到空表，
// 读写重叠被重建（一个 guard 只会各写一次，写侧很快就静默，用例只剩读）。
func runGuardWave(t *testing.T, g *WorkspaceGuard, probes []guardAuthProbe, wave int, answer string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < guardWaveSpinners; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start

			// 单调性判据：同一份越界参数先放行、后又要求授权，只可能是并发下读到了
			// "拼出来的假状态"—— bypass 一旦被置真就没有回去的路径（yolo 那条被
			// 独立的写侧 goroutine 恒写 false，也不会翻回来）。
			sawBypass := make(map[string]bool, len(probes))

			for it := 0; it < guardWaveIters; it++ {
				p := probes[(idx+it)%len(probes)]

				needs, reason := g.Check(p.tool, p.params)
				if needs && reason == "" {
					t.Errorf("wave %d: Check(%s) 要求授权却没给用户可读的理由", wave, p.tool)
					return
				}
				if needs && sawBypass[p.tool] {
					t.Errorf("wave %d(%s): Check(%s) 已放行后又要求授权 —— 读到拼出来的假状态",
						wave, answer, p.tool)
					return
				}
				if !needs {
					sawBypass[p.tool] = true
				}

				if it%2 == 0 {
					if err := g.RequestAuth(ctx, p.tool, "并发用例的授权请求"); err != nil {
						t.Errorf("wave %d(%s): RequestAuth(%s) = %v, want nil",
							wave, answer, p.tool, err)
						return
					}
				}
			}
		}(i)
	}

	// 独立的 flag 写侧：SetYoloMode(false) 语义上是 no-op，但每次都是一次与
	// authBypassed 读侧竞争的写 —— 去掉锁时这一条也红。
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < guardWaveSpinners*guardWaveIters/4; i++ {
			g.SetYoloMode(false)
		}
	}()

	close(start)

	// 完成信号：WaitGroup + 带超时的 select（不允许拿 time.Sleep 当同步）。
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatalf("wave %d: 并发阶段没有结束 —— 疑似把等用户的 RequestConfirmation 包进了锁里", wave)
	}
}

// TestWorkspaceGuard_ConcurrentCheckAndRequestAuth 钉住"授权状态可以被并行委派并发读写"
// 这件事。三个分支（allow_session / allow_all_session / allow_all_project）各跑一波，
// 外加 allow_session 跑两波（那是唯一会并发写 map 的分支）。
func TestWorkspaceGuard_ConcurrentCheckAndRequestAuth(t *testing.T) {
	// HOME 必须换到临时目录：loadProjectAuth 读 $HOME/.yagent/settings.json，
	// 开发机上这个项目若已永久授权，Check 永远返回 (false, "")，用例就退化成恒真；
	// 顺带把 allow_all_project 的落盘写进临时目录而不是用户真实配置。
	t.Setenv("HOME", t.TempDir())

	work := t.TempDir()
	// 与 workspace 同级、因此在工作区外部的路径 —— 判据是"越界"而不是"写操作"。
	outside := filepath.Join(filepath.Dir(work), "guard-concurrent-outside.txt")

	probes := []guardAuthProbe{
		{tool: "create_file", params: map[string]interface{}{"target_file": outside, "content": "x"}},
		{tool: "delete_file", params: map[string]interface{}{"file_paths": []interface{}{outside}}},
		{tool: "run_bash", params: map[string]interface{}{"command": "echo hi", "is_dangerous": true}},
		{tool: "browser_eval", params: map[string]interface{}{"js": "document.cookie"}},
	}

	h := newGuardAuthHarness(t)

	answers := []string{"allow_session", "allow_session", "allow_all_session", "allow_all_project"}
	for wave, ans := range answers {
		answer := ans
		h.responder.answer.Store(&answer)

		g := NewWorkspaceGuard(work, h.confirm)
		runGuardWave(t, g, probes, wave, answer)

		// 收尾断言（产品语义，不只是"没崩"）：每个用过的工具再 Check 一次必须是
		// (false, "") —— 会话/全部/项目三级授权都真的生效了。
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		t.Cleanup(cancel)

		askedBefore := h.responder.asked.Load()
		for _, p := range probes {
			if err := g.RequestAuth(ctx, p.tool, "收尾再请求一次"); err != nil {
				t.Errorf("wave %d(%s): 收尾 RequestAuth(%s) = %v, want nil（已授权不该报错）",
					wave, answer, p.tool, err)
			}
			if needs, reason := g.Check(p.tool, p.params); needs || reason != "" {
				t.Errorf("wave %d(%s): 收尾 Check(%s) = (%v, %q), want (false, \"\")",
					wave, answer, p.tool, needs, reason)
			}
		}
		// 并发阶段已经把四个探针各自授权过了，收尾再弹框就是"同一件事问两次"的骚扰。
		if extra := h.responder.asked.Load() - askedBefore; extra != 0 {
			t.Errorf("wave %d(%s): 已授权之后仍然弹了 %d 次授权框, want 0", wave, answer, extra)
		}
	}

	// allow_all_project 那一波必须把永久授权原子落盘（saveProjectAuth 转调
	// util.AtomicWrite 之后：读者看不到半份 JSON，目录里也不留 .tmp 中间态）。
	settingsPath := filepath.Join(os.Getenv("HOME"), ".yagent", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("allow_all_project 之后 %s 应当存在: %v", settingsPath, err)
	}
	var settings struct {
		AuthorizedProjects map[string]bool `json:"authorized_projects"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("settings.json 必须是完整 JSON（半份文件会让下一次 loadProjectAuth 静默失败）: %v", err)
	}
	if !settings.AuthorizedProjects[filepath.Clean(work)] {
		t.Errorf("settings.json 缺少本次工作区的授权: %+v", settings.AuthorizedProjects)
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(settingsPath), "*.tmp"))
	if len(leftovers) != 0 {
		t.Errorf("原子落盘留下中间态: %v", leftovers)
	}
}
