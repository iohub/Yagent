package config

import (
	"fmt"
	"testing"
)

// max_in_flight_requests 的 0 有语义（"不限"），而"没写这个键"解析出来同样是 0 ——
// 这正是本仓反复踩过的坑（§11 坑 26），所以缺省判定必须看键的存在性。
// 这里钉的是四种写法各自落成什么数，负数按"没写"处理。
//
// 期望值写**字面量**而不是 DefaultMaxInFlightRequests：引用常量的话，把默认值改成
// 0（=完全不限流）这条测试照样绿，而"默认配置该带 429 保护"正是它要守住的东西。
func TestLoadFromFile_MaxInFlightRequests(t *testing.T) {
	if DefaultMaxInFlightRequests != 8 {
		t.Errorf("DefaultMaxInFlightRequests = %d, want 8", DefaultMaxInFlightRequests)
	}
	for _, arm := range []struct {
		line string
		want int
	}{
		{"", 8},
		{"max_in_flight_requests = 0", 0},
		{"max_in_flight_requests = 3", 3},
		{"max_in_flight_requests = -1", 8},
	} {
		body := twoProviders + fmt.Sprintf("\n[llm]\n%s\n", arm.line)
		cfg, err := LoadFromFile(writeCfg(t, body))
		if err != nil {
			t.Fatalf("line %q: load: %v", arm.line, err)
		}
		if cfg.LLM.MaxInFlightRequests != arm.want {
			t.Errorf("line %q → MaxInFlightRequests = %d, want %d", arm.line, cfg.LLM.MaxInFlightRequests, arm.want)
		}
	}
}

// 整段 [llm] 都没写时也必须拿到默认上限，而不是 0（=不限）：
// 新机器上的默认配置就该带着 429 保护。
func TestLoadFromFile_InFlightDefaultWithoutLLMSection(t *testing.T) {
	cfg, err := LoadFromFile(writeCfg(t, twoProviders))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.LLM.MaxInFlightRequests != 8 {
		t.Errorf("没有 [llm] 段时 MaxInFlightRequests = %d, want 8", cfg.LLM.MaxInFlightRequests)
	}
}
