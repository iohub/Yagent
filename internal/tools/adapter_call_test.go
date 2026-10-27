package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// Adapter.Call 的第 1 步（JSON 解析）是成文契约：internal/tools/AGENTS.md 硬约束 9
// ——解析失败**不报错**，回落成单参数 {"input": 原始串}，四步顺序不得增删。
// 本文件钉三件事：
//  1. 兜底形状仍然成立（半截 JSON、裸字符串都不该变成 error）；
//  2. 字面量 "null" 也必须走兜底 —— 它 unmarshal 成功却得到 nil map，旧写法让它
//     绕过兜底，于是必填参数静默缺失、报错信息指不到真相；
//  3. 兜底发生时必须有一条 Warn 日志（工具名 + 原始入参线索），否则"参数整个丢了"
//     与"工具本来就不要参数"在日志里长得一样。

// echoAdapter 把收到的参数表原样返回：结果 JSON 就是执行器真正看到的 params。
func echoAdapter(name string) *Adapter {
	return NewAdapter(name, "echo fixture", func(_ context.Context, params map[string]interface{}) (interface{}, error) {
		return params, nil
	})
}

// callParams 跑一次 Call 并把返回值解回 map；返回的 map 即执行器收到的参数表。
func callParams(t *testing.T, a *Adapter, input string) map[string]interface{} {
	t.Helper()
	got, err := a.Call(context.Background(), input)
	if err != nil {
		t.Fatalf("硬约束 9：入参不可用不得变成 error，got err = %v", err)
	}
	var params map[string]interface{}
	if err := json.Unmarshal([]byte(got), &params); err != nil {
		t.Fatalf("返回值不是对象: %q (%v)", got, err)
	}
	return params
}

func assertFallbackShape(t *testing.T, params map[string]interface{}, raw string) {
	t.Helper()
	if len(params) != 1 {
		t.Fatalf("参数表 = %v, want 只有一个键", params)
	}
	if got, ok := params["input"]; !ok || got != raw {
		t.Errorf(`params = %v, want {"input": %q}`, params, raw)
	}
}

// 半截 JSON（max_tokens 截断的真实形状）：走兜底、不报错。
func TestAdapterCall_MalformedJSON_FallsBackToInputParam(t *testing.T) {
	raw := `{"path":"a.go","content":"func ma`
	assertFallbackShape(t, callParams(t, echoAdapter("create_file"), raw), raw)
}

// 字面量 null：unmarshal 成功但得到 nil map，必须同样走兜底（问题 1 的洞）。
func TestAdapterCall_NullInput_FallsBackToInputParam(t *testing.T) {
	assertFallbackShape(t, callParams(t, echoAdapter("agent_exit"), "null"), "null")
}

// 空串（provider 压根没发 arguments 的形状）：同样走兜底。
func TestAdapterCall_EmptyInput_FallsBackToInputParam(t *testing.T) {
	assertFallbackShape(t, callParams(t, echoAdapter("agent_exit"), ""), "")
}

// 合法对象逐键直达执行器，不得被兜底吃掉，也不得多出 "input" 键。
func TestAdapterCall_ValidObject_UsesParamsDirectly(t *testing.T) {
	params := callParams(t, echoAdapter("read_file"), `{"target_file":"a.go","explanation":"why"}`)
	if _, ok := params["input"]; ok {
		t.Fatalf(`合法对象走了兜底: %v`, params)
	}
	if params["target_file"] != "a.go" || params["explanation"] != "why" {
		t.Errorf("params = %v, want 两个键逐字到达", params)
	}
}

// ---------- 兜底日志 ----------

// logCapture 是一个把 slog 记录收进切片的 handler；slog 默认 logger 是进程级的，
// 用例结束时必须还原（本包的测试都不带 t.Parallel，替换窗口内没有并发写入）。
type logCapture struct {
	mu    sync.Mutex
	calls []capturedLog
}

type capturedLog struct {
	msg   string
	attrs map[string]any
}

func (c *logCapture) Enabled(context.Context, slog.Level) bool { return true }

func (c *logCapture) Handle(_ context.Context, rec slog.Record) error {
	attrs := make(map[string]any, rec.NumAttrs())
	rec.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, capturedLog{msg: rec.Message, attrs: attrs})
	return nil
}

func (c *logCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *logCapture) WithGroup(string) slog.Handler      { return c }

func (c *logCapture) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = nil
}

func useCapture(t *testing.T) *logCapture {
	t.Helper()
	logs := &logCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(logs))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return logs
}

func (c *logCapture) oneWarn(t *testing.T) capturedLog {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.calls) != 1 {
		t.Fatalf("Warn 条数 = %d, want 恰好 1 条（兜底要留痕，但一次调用只留一条）", len(c.calls))
	}
	return c.calls[0]
}

func TestAdapterCall_FallbackWarnsWithToolNameAndRaw(t *testing.T) {
	logs := useCapture(t)

	raw := `{"content":"` + strings.Repeat("x", 900)
	if _, err := echoAdapter("create_file").Call(context.Background(), raw); err != nil {
		t.Fatalf("got err = %v", err)
	}

	rec := logs.oneWarn(t)
	if !strings.Contains(rec.msg, "tool arguments are not a usable JSON object") ||
		!strings.Contains(rec.msg, "falling back to single input param") {
		t.Errorf("日志文案 = %q, want 点明不可用与兜底动作", rec.msg)
	}
	if rec.attrs["tool"] != "create_file" {
		t.Errorf(`attr "tool" = %v, want "create_file"`, rec.attrs["tool"])
	}
	// slog 的 Value.Any() 对整型返回 int64，不是 int。
	if rec.attrs["raw_len"] != int64(len(raw)) {
		t.Errorf(`attr "raw_len" = %v, want %d`, rec.attrs["raw_len"], len(raw))
	}
	gotRaw, ok := rec.attrs["raw"].(string)
	if !ok {
		t.Fatalf(`attr "raw" = %T, want string`, rec.attrs["raw"])
	}
	if len(gotRaw) > maxFallbackRawLogBytes+64 {
		t.Errorf("attr \"raw\" 长度 = %d, want 截进预算（+%d 的省略标记）", len(gotRaw), 64)
	}
	if !strings.Contains(gotRaw, "truncated") {
		t.Errorf(`attr "raw" = %q, want 带省略标记`, gotRaw)
	}

	// 合法参数不许刷屏：兜底日志只属于兜底。
	logs.reset()
	if _, err := echoAdapter("read_file").Call(context.Background(), `{"target_file":"a.go"}`); err != nil {
		t.Fatalf("got err = %v", err)
	}
	logs.mu.Lock()
	n := len(logs.calls)
	logs.mu.Unlock()
	if n != 0 {
		t.Errorf("合法入参产生了 %d 条日志, want 0 条", n)
	}
}

func TestTruncateRawForLog_KeepsWholeRunesAndShortInput(t *testing.T) {
	if got := truncateRawForLog(`{"a":1}`); got != `{"a":1}` {
		t.Errorf("预算内的输入被改写: %q", got)
	}
	if got := truncateRawForLog(""); got != "" {
		t.Errorf("空输入 = %q, want 空串", got)
	}

	// 预算正好落在一个 3 字节汉字中间：按字节硬切会在日志里留下半个字。
	raw := strings.Repeat("字", 400) // 1200 字节
	got := truncateRawForLog(raw)
	if !utf8.ValidString(got) {
		t.Errorf("截断结果不是合法 UTF-8: %q", got)
	}
	if !strings.HasPrefix(got, "字") || !strings.Contains(got, "truncated") {
		t.Errorf("截断结果 = %q, want 原文前缀 + 省略标记", got)
	}
	if strings.Count(got, "字") != maxFallbackRawLogBytes/3 {
		t.Errorf("保留的字数 = %d, want %d", strings.Count(got, "字"), maxFallbackRawLogBytes/3)
	}
}
