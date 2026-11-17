package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 这三个旋钮都有一条"配错了会静默变坏"的路径：
//   - stream_idle_timeout 写成裸整数（90）会被解码成 90 **纳秒**，每个流当场判死；
//   - 0 / 负数没有"关掉"的语义（关掉就等于恢复永久阻塞）；
//   - retry_attempt_timeout 的 0 反而有语义（不额外限制），判据必须与前三者分开。
// 所以缺省归一化与"写错单位"的告警都必须在 validate() 里有一条可钉的判据。

func TestLoadFromFile_StreamTimeouts(t *testing.T) {
	cases := []struct {
		name                string
		llmBody             string
		wantIdle, wantFirst time.Duration
		wantAttempt         time.Duration
	}{
		{
			name:        "写了就用写的",
			llmBody:     "stream_idle_timeout = \"45s\"\nfirst_byte_timeout = \"20s\"\nretry_attempt_timeout = \"3m\"\n",
			wantIdle:    45 * time.Second,
			wantFirst:   20 * time.Second,
			wantAttempt: 3 * time.Minute,
		},
		{
			name:        "没写 → 程序保证默认值",
			llmBody:     "",
			wantIdle:    DefaultLLMStreamIdleTimeout,
			wantFirst:   DefaultLLMFirstByteTimeout,
			wantAttempt: 0,
		},
		{
			name:        "显式 0 不能关掉看门狗",
			llmBody:     "stream_idle_timeout = \"0s\"\nfirst_byte_timeout = \"0s\"\nretry_attempt_timeout = \"0s\"\n",
			wantIdle:    DefaultLLMStreamIdleTimeout,
			wantFirst:   DefaultLLMFirstByteTimeout,
			wantAttempt: 0,
		},
		{
			name:        "裸整数（被解码成纳秒）按没配处理",
			llmBody:     "stream_idle_timeout = 90\nfirst_byte_timeout = 60\n",
			wantIdle:    DefaultLLMStreamIdleTimeout,
			wantFirst:   DefaultLLMFirstByteTimeout,
			wantAttempt: 0,
		},
		{
			name:        "负值按没配处理",
			llmBody:     "stream_idle_timeout = \"-30s\"\nfirst_byte_timeout = \"-1s\"\nretry_attempt_timeout = \"-2m\"\n",
			wantIdle:    DefaultLLMStreamIdleTimeout,
			wantFirst:   DefaultLLMFirstByteTimeout,
			wantAttempt: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := twoProviders
			if tc.llmBody != "" {
				body += "\n[llm]\n" + tc.llmBody
			}
			cfg, err := LoadFromFile(writeCfg(t, body))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if cfg.LLM.StreamIdleTimeout != tc.wantIdle {
				t.Errorf("StreamIdleTimeout = %v, want %v", cfg.LLM.StreamIdleTimeout, tc.wantIdle)
			}
			if cfg.LLM.FirstByteTimeout != tc.wantFirst {
				t.Errorf("FirstByteTimeout = %v, want %v", cfg.LLM.FirstByteTimeout, tc.wantFirst)
			}
			if cfg.LLM.RetryAttemptTimeout != tc.wantAttempt {
				t.Errorf("RetryAttemptTimeout = %v, want %v", cfg.LLM.RetryAttemptTimeout, tc.wantAttempt)
			}
		})
	}
}

// saveStreamGlobals 保存并在用后恢复两个全局值，同时把当前值归零 ——
// 读取口"没有全局值时回落默认"的那条判据只有在全局值确实为 0 时才成立，
// 而 Go 测试是并发不可控的：不隔离就会变成"跑第二条用例时上一条的值还在"
//（-count=2 会当场抓到）。0 在 Set* 里有"拒绝"语义，所以这里直接动原子量。
func saveStreamGlobals(t *testing.T) {
	t.Helper()
	prevIdle := globalStreamIdleTimeoutNS.Load()
	prevFirst := globalFirstByteTimeoutNS.Load()
	t.Cleanup(func() {
		globalStreamIdleTimeoutNS.Store(prevIdle)
		globalFirstByteTimeoutNS.Store(prevFirst)
	})
	globalStreamIdleTimeoutNS.Store(0)
	globalFirstByteTimeoutNS.Store(0)
}

// 读取口必须恒返回正数：引擎直接把它交给看门狗，拿到 0 就等于把看门狗关掉。
func TestEffectiveStreamTimeoutsNeverReturnZero(t *testing.T) {
	saveStreamGlobals(t)

	if got := EffectiveStreamIdleTimeout(0); got != DefaultLLMStreamIdleTimeout {
		t.Errorf("EffectiveStreamIdleTimeout(0) = %v, want 默认值", got)
	}
	if got := EffectiveStreamIdleTimeout(-time.Minute); got != DefaultLLMStreamIdleTimeout {
		t.Errorf("EffectiveStreamIdleTimeout(负数) = %v, want 默认值", got)
	}
	if got := EffectiveStreamIdleTimeout(42 * time.Second); got != 42*time.Second {
		t.Errorf("EffectiveStreamIdleTimeout(显式值) = %v, want 42s", got)
	}
	if got := EffectiveFirstByteTimeout(0); got != DefaultLLMFirstByteTimeout {
		t.Errorf("EffectiveFirstByteTimeout(0) = %v, want 默认值", got)
	}
	if got := EffectiveFirstByteTimeout(5 * time.Second); got != 5*time.Second {
		t.Errorf("EffectiveFirstByteTimeout(显式值) = %v, want 5s", got)
	}
}

// 全局值的写入口拒绝 0/负数，并保留现值（理由同 SetGlobalLLMTimeout）。
func TestSetGlobalStreamTimeoutsRefusesDisable(t *testing.T) {
	saveStreamGlobals(t)

	SetGlobalStreamIdleTimeout(30 * time.Second)
	SetGlobalStreamIdleTimeout(0)
	SetGlobalStreamIdleTimeout(-time.Second)
	if got := GlobalStreamIdleTimeout(); got != 30*time.Second {
		t.Errorf("GlobalStreamIdleTimeout() = %v, want 30s（0/负数必须被拒绝）", got)
	}

	SetGlobalFirstByteTimeout(15 * time.Second)
	SetGlobalFirstByteTimeout(0)
	if got := GlobalFirstByteTimeout(); got != 15*time.Second {
		t.Errorf("GlobalFirstByteTimeout() = %v, want 15s", got)
	}

	// 没配显式值时全局值参与兜底……
	if got := EffectiveStreamIdleTimeout(0); got != 30*time.Second {
		t.Errorf("EffectiveStreamIdleTimeout(0) = %v, want 全局值 30s（零值 LLMConfig 的兜底路径）", got)
	}
	// ……但显式配置优先：全局值只服务"引擎拿到零值 LLMConfig"的路径。
	if got := EffectiveStreamIdleTimeout(7 * time.Second); got != 7*time.Second {
		t.Errorf("EffectiveStreamIdleTimeout(显式值) = %v, want 7s（全局值不得覆盖显式配置）", got)
	}
}

// 模板是用户发现这些键存在的唯一入口；template_test.go 只保证"写了能被解析"。
func TestStreamTimeoutsAreInTemplates(t *testing.T) {
	for _, path := range []string{"default_config.toml", filepath.Join("..", "..", "config", "config.toml")} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Logf("跳过 %s: %v", path, err)
			continue
		}
		body := string(raw)
		for _, key := range []string{"stream_idle_timeout", "first_byte_timeout", "retry_attempt_timeout"} {
			if !strings.Contains(body, key) {
				t.Errorf("%s 里没有 %s（写了没人读/能读但没人发现都算静默）", path, key)
			}
		}
	}
}
