package llm

// client_switch_test.go — 运行中换 provider 的线程安全。
//
// TUI 现在允许在任务跑着的时候切模型，于是"替换默认引擎 / 清空按 provider 缓存"
// 与"某个工作线程正在解析引擎"会同时发生。接口值是类型字+数据字两个机器字，
// 裸读可能读到撕裂的一半，所以这些读一律要过锁。

import (
	"context"
	"sync"
	"testing"

	"yagent/internal/config"
)

func switchTestClient() *Client {
	return &Client{
		Engine:  &fakeEngine{model: "alpha-model"},
		engines: map[string]Engine{},
		Config: &config.Config{Global: config.TopLevelConfig{LLM: &config.GlobalLLMConfig{
			UseProvider: "alpha",
			Providers: map[string]config.ProviderConfig{
				"alpha": {Model: "alpha-model", APIBaseURL: "http://127.0.0.1:1/v1", APIKey: "k"},
				"beta":  {Model: "beta-model", APIBaseURL: "http://127.0.0.1:1/v1", APIKey: "k"},
			},
		}}},
	}
}

func TestClient_SwitchProviderWhileAgentsResolve(t *testing.T) {
	c := switchTestClient()

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 40; j++ {
				if got := c.GetAgentEngine("coding").Model(); got != "alpha-model" && got != "beta-model" {
					t.Errorf("GetAgentEngine(coding).Model() = %q, want one of the two configured models", got)
					return
				}
				if eng := c.DefaultEngine(); eng != nil {
					_ = eng.Model()
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 40; j++ {
			want := "beta"
			if j%2 == 0 {
				want = "alpha"
			}
			if err := c.SwitchProvider(want); err != nil {
				t.Errorf("SwitchProvider(%q): %v", want, err)
				return
			}
		}
	}()
	wg.Wait()

	if got := c.DefaultEngine().Model(); got != "beta-model" {
		t.Errorf("final DefaultEngine() = %q, want the last switched model", got)
	}
}

// SetAgentProvider 与解析并发：覆盖表本身有锁，解析必须在这把锁的另一侧读。
func TestClient_SetAgentProviderWhileResolving(t *testing.T) {
	c := switchTestClient()
	c.setRouter(&Router{
		enabled: true,
		engines: [3]Engine{
			&fakeEngine{model: "fast-model"},
			&fakeEngine{model: "bal-model"},
			&fakeEngine{model: "high-model"},
		},
		baselines: map[string]Tier{"coding": TierBalanced},
	})
	re := NewRoutingEngine(c, "coding")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 40; j++ {
			if err := c.SetAgentProvider("coding", "alpha"); err != nil {
				t.Errorf("SetAgentProvider: %v", err)
				return
			}
			if !c.HasAgentOverride("coding") {
				t.Error("HasAgentOverride = false，但覆盖刚被设置")
				return
			}
			if err := c.SetAgentProvider("coding", "beta"); err != nil {
				t.Errorf("SetAgentProvider: %v", err)
				return
			}
		}
	}()
	for j := 0; j < 40; j++ {
		eng := re.resolve(context.Background())
		if eng == nil {
			t.Fatal("resolve returned a nil engine — 覆盖与档位都没命中时会在这里 panic")
		}
	}
	wg.Wait()
}
