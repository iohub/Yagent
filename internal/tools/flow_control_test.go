package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// agent_exit / ask_user_for_help 的报错是这条链上唯一被人和机器同时读到的东西，
// 所以它必须说清两件事：**哪一种**故障（参数没给 vs 给了但不是 string），以及来自
// 哪个**真实工具名**。旧写法把两者都糊掉了：`params["reason"].(string)` 的 ok==false
// 同时覆盖"键不存在"与"类型不对"，标签还是历史 Go 方法名 "executeFinish"
// （真实名字是 agent_exit），于是排查方向被带偏。
//
// 注：util.WrapError 把第三个参数拼成 "<message>: <err>"（internal/util/error_utils.go:88-100），
// 所以工具名前缀由它提供，内层文案不再重复写 "agent_exit"，否则会打成
// "agent_exit: agent_exit: ..."。下面断言的是最终可见的整串。

func decodeParams(t *testing.T, raw string) map[string]interface{} {
	t.Helper()
	var params map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		t.Fatalf("fixture 不是合法 JSON: %q (%v)", raw, err)
	}
	return params
}

// 键缺失（含显式 null）必须报成 "missing"，而不是 "must be a string"。
func TestFlowControl_MissingReason_DistinctMessage(t *testing.T) {
	tool := NewFlowControlTool(t.TempDir())
	cases := map[string]map[string]interface{}{
		"空参数表": {},
		// arguments 为 "null" 时 Adapter 的兜底给出 {"input":"null"}：reason 键同样不在。
		"显式 null": decodeParams(t, `{"reason":null}`),
	}
	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := tool.ExecuteAgentExit(context.Background(), params); err == nil {
				t.Fatal("缺少 reason 却成功了")
			} else {
				msg := err.Error()
				if !strings.Contains(msg, "agent_exit") {
					t.Errorf("错误里看不到真实工具名: %q", msg)
				}
				if strings.Contains(msg, "executeFinish") {
					t.Errorf("旧方法名标签残留: %q", msg)
				}
				if !strings.Contains(msg, "missing") {
					t.Errorf("want 指向\"参数没给\", got %q", msg)
				}
				if strings.Contains(msg, "must be a string") {
					t.Errorf("键缺失被报成了类型错误: %q", msg)
				}
			}
		})
	}
}

// 类型不对必须报出实际类型名，并且不许和"键缺失"共用一条消息。
func TestFlowControl_WrongType_DistinctMessage(t *testing.T) {
	// 走真实解码路径：JSON 数字进 Go 是 float64，报错就该写 float64 而不是 "number"。
	params := decodeParams(t, `{"reason":123}`)
	if _, err := NewFlowControlTool(t.TempDir()).ExecuteAgentExit(context.Background(), params); err == nil {
		t.Fatal("reason 不是 string 却成功了")
	} else {
		msg := err.Error()
		if !strings.Contains(msg, "must be a string") {
			t.Errorf("want 指向类型错误, got %q", msg)
		}
		if !strings.Contains(msg, "float64") {
			t.Errorf("错误里没给出实际类型名: %q", msg)
		}
		if !strings.Contains(msg, "agent_exit") || strings.Contains(msg, "executeFinish") {
			t.Errorf("工具名标签不对: %q", msg)
		}
		if strings.Contains(msg, "missing") {
			t.Errorf("类型错误被报成了缺参数: %q", msg)
		}
	}
}

// 正常路径不许被诊断改动碰坏。
func TestFlowControl_ValidReason_Succeeds(t *testing.T) {
	got, err := NewFlowControlTool(t.TempDir()).ExecuteAgentExit(context.Background(), map[string]interface{}{"reason": "done"})
	if err != nil {
		t.Fatalf("got err = %v", err)
	}
	result, ok := got.(map[string]interface{})
	if !ok {
		t.Fatalf("返回值 = %T, want map", got)
	}
	if result["finished"] != true {
		t.Errorf(`result["finished"] = %v, want true`, result["finished"])
	}
	if result["reason"] != "done" {
		t.Errorf(`result["reason"] = %v, want "done"`, result["reason"])
	}
}

// 硬约束 5：handoff / blueprint 在工具层只回显、不解析。
func TestAgentExit_HandoffAndBlueprint_EchoedUnchanged(t *testing.T) {
	handoff := map[string]interface{}{
		"conclusion": "fixed it",
		"facts":      []interface{}{map[string]interface{}{"claim": "a", "where": "f.go:1"}},
		"status":     "ok",
	}
	blueprint := []interface{}{
		map[string]interface{}{"section": "modules", "module": "internal/tools", "claim": "x"},
	}
	params := map[string]interface{}{"reason": "done", "handoff": handoff, "blueprint": blueprint}

	got, err := NewFlowControlTool(t.TempDir()).ExecuteAgentExit(context.Background(), params)
	if err != nil {
		t.Fatalf("got err = %v", err)
	}
	result := got.(map[string]interface{})
	if !reflect.DeepEqual(result["handoff"], handoff) {
		t.Errorf("handoff 被改写了: %v", result["handoff"])
	}
	if !reflect.DeepEqual(result["blueprint"], blueprint) {
		t.Errorf("blueprint 被改写了: %v", result["blueprint"])
	}
	if result["reason"] != "done" {
		t.Errorf("reason = %v", result["reason"])
	}

	// 没给的两个键不许凭空出现：Agent 侧靠"键在不在"区分"没交"与"交了空的"。
	empty, err := NewFlowControlTool(t.TempDir()).ExecuteAgentExit(context.Background(), map[string]interface{}{"reason": "done"})
	if err != nil {
		t.Fatalf("got err = %v", err)
	}
	if _, ok := empty.(map[string]interface{})["handoff"]; ok {
		t.Error("没给 handoff 却回显了空值")
	}
}

// 整条链的回归：arguments 为字面量 "null" 时，报错必须能指回"参数没给"。
// 旧形状是 Adapter 兜底没触发 → nil map → "reason parameter must be a string"，
// 一句把排查方向从"上游给了个不可用的 arguments"带到"模型写错了类型"。
func TestAgentExit_NullArguments_ProduceDiagnosableMessage(t *testing.T) {
	a := NewAdapter("agent_exit", "exit fixture", NewFlowControlTool(t.TempDir()).ExecuteAgentExit)
	_, err := a.Call(context.Background(), "null")
	if err == nil {
		t.Fatal("arguments 是 null 却成功了")
	}
	msg := err.Error()
	if !strings.Contains(msg, "agent_exit") || !strings.Contains(msg, "missing") {
		t.Errorf("msg = %q, want 同时含真实工具名与 missing", msg)
	}
	if strings.Contains(msg, "must be a string") {
		t.Errorf("msg = %q, want 不把\"参数整个丢了\"报成类型错误", msg)
	}
}

// ask_user_for_help 的标签同样必须是真实工具名。
func TestAskUserForHelp_UsesRealToolName(t *testing.T) {
	if _, err := NewFlowControlTool(t.TempDir()).ExecuteAskUserForHelp(context.Background(), map[string]interface{}{}); err == nil {
		t.Fatal("缺 reason 却成功了")
	} else {
		msg := err.Error()
		if !strings.Contains(msg, "ask_user_for_help") || strings.Contains(msg, "executeAskUserForHelp") {
			t.Errorf("工具名标签不对: %q", msg)
		}
	}
}
