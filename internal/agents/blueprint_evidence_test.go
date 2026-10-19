package agents

import (
	"encoding/json"
	"strings"
	"testing"

	"yagent/internal/llm"
)

// 本文件钉 §7.5 第 2 条的证据采集：入参侧与**出参侧**都要算。
//
// v3 的理由照抄：只认入参，一轮"检索到 8 个文件、读过其中 3 个"的分析就只能提交 3 片，
// 而那 8 片才是它真正掌握的 —— 只做入参侧的校验是个会误伤的校验。
// 反向也钉：没有提交时**不扫 History**（每条委派都扫一遍是把 R6 的成本摊到每一轮）。

// toolCallFixture 是一条"模型调了某个工具"的夹具（工具名 + 参数）。
// 名字避开 scriptedCall —— 那是 e2e 脚本引擎记录**真实调用**的类型，同包不能重名。
type toolCallFixture struct {
	name string
	args map[string]interface{}
}

// assistantCalls 造一条带工具调用的 assistant 消息。
func assistantCalls(calls ...toolCallFixture) llm.Message {
	m := llm.Message{Role: llm.RoleAssistant}
	for i, c := range calls {
		b, _ := json.Marshal(c.args)
		m.ToolCalls = append(m.ToolCalls, llm.ToolCall{
			ID:       "call_" + string(rune('a'+i)),
			Type:     "function",
			Function: llm.FunctionCall{Name: c.name, Arguments: string(b)},
		})
	}
	return m
}

func toolResult(name, content string) llm.Message {
	return llm.Message{Role: llm.RoleTool, ToolName: name, Content: content}
}

func exitText(items ...map[string]interface{}) string {
	b, _ := json.Marshal(map[string]interface{}{
		"finished": true, "reason": "done", "blueprint": items,
	})
	return string(b)
}

// 出参侧：只检索、没逐个 read_file，也必须把命中文件算进证据集。
func TestEvidenceSetIncludesSearchResults(t *testing.T) {
	hits := map[string]interface{}{"hits": []interface{}{
		map[string]interface{}{"file_path": "internal/a/a.go", "score": 0.9},
		map[string]interface{}{"file_path": "internal/b/b.go", "score": 0.8},
	}}
	payload, _ := json.Marshal(hits)
	exec := ExecutorResult{
		Text: exitText(map[string]interface{}{
			"section": "modules", "module": "internal/a", "field": "purpose", "claim": "a claim",
		}),
		History: []llm.Message{
			assistantCalls(toolCallFixture{"semantic_search", map[string]interface{}{"query": "alpha"}}),
			toolResult("semantic_search", string(payload)),
		},
	}

	items, files := collectBlueprintSubmission(exec)
	if len(items) != 1 {
		t.Fatalf("提交没被采集: %+v", items)
	}
	joined := strings.Join(files, ",")
	if !strings.Contains(joined, "internal/a/a.go") || !strings.Contains(joined, "internal/b/b.go") {
		t.Errorf("出参侧的命中文件没进证据集（只认入参的校验会误伤这一轮）: %q", joined)
	}
}

// 入参侧：不同工具用不同的键名放路径，采集必须认全（漏一个键 = 那条路径白读了）。
func TestEvidenceSetReadsAllPathParams(t *testing.T) {
	calls := []struct {
		tool string
		args map[string]interface{}
		want string
	}{
		{"read_file", map[string]interface{}{"target_file": "internal/a/a.go"}, "internal/a/a.go"},
		{"search_replace_in_file", map[string]interface{}{"path": "internal/b/b.go"}, "internal/b/b.go"},
		{"rename_file", map[string]interface{}{"old_path": "internal/c/c.go", "new_path": "internal/c/d.go"}, "internal/c/c.go"},
		{"print_dir_tree", map[string]interface{}{"paths": []interface{}{"internal/d/d.go"}}, "internal/d/d.go"},
		{"search_by_regex", map[string]interface{}{"regex": "Alpha"}, "internal/e/e.go"},
	}
	msg := assistantCalls()
	for _, c := range calls {
		b, _ := json.Marshal(c.args)
		msg.ToolCalls = append(msg.ToolCalls, llm.ToolCall{
			ID: "id_" + c.want, Type: "function",
			Function: llm.FunctionCall{Name: c.tool, Arguments: string(b)},
		})
	}
	history := []llm.Message{msg}
	// search_by_regex 的命中行形态（`path:line:` 文本，不是 JSON）。
	history = append(history, toolResult("search_by_regex", "internal/e/e.go:12: func Alpha() {}\n"))

	exec := ExecutorResult{
		Text:    exitText(map[string]interface{}{"section": "gotchas", "claim": "x"}),
		History: history,
	}
	_, files := collectBlueprintSubmission(exec)
	got := strings.Join(files, ",")
	for _, want := range []string{"internal/a/a.go", "internal/b/b.go", "internal/c/c.go", "internal/d/d.go", "internal/e/e.go"} {
		if !strings.Contains(got, want) {
			t.Errorf("证据集少了 %q（该工具的路径参数没被认出来）: %q", want, got)
		}
	}
}

// 没有提交就不该扫 History —— 但**扫描本身也绝不能丢提交**。
// 这条钉住两个方向：无提交 → 零开销；有提交但收尾不是 agent_exit → 不采集。
func TestEvidenceCollectedOnlyForRealSubmissions(t *testing.T) {
	history := []llm.Message{
		assistantCalls(toolCallFixture{"read_file", map[string]interface{}{"target_file": "internal/a/a.go"}}),
	}

	items, files := collectBlueprintSubmission(ExecutorResult{
		Text: "一段普通回复，没有 agent_exit", History: history,
	})
	if items != nil || files != nil {
		t.Errorf("非 agent_exit 的文本被当成提交了: items=%v files=%v", items, files)
	}

	items, files = collectBlueprintSubmission(ExecutorResult{
		Text: `{"finished":true,"reason":"done"}`, History: history,
	})
	if len(items) != 0 {
		t.Errorf("没有 blueprint 键却造出了提交: %+v", items)
	}
	if files != nil {
		t.Errorf("没有提交仍然扫了 History（R6 的成本被摊到每一轮）: %v", files)
	}
}

// 提交的证据要能穿过真实工具名的形态：run_bash 的输出不是路径清单，不该被当成读过。
func TestEvidenceIgnoresNonPathToolOutput(t *testing.T) {
	exec := ExecutorResult{
		Text: exitText(map[string]interface{}{"section": "gotchas", "claim": "x"}),
		History: []llm.Message{
			assistantCalls(toolCallFixture{"run_bash", map[string]interface{}{"command": "ls internal/a"}}),
			toolResult("run_bash", "a.go\nfill01.go\n"),
		},
	}
	_, files := collectBlueprintSubmission(exec)
	for _, f := range files {
		if strings.Contains(f, "internal/a") {
			t.Errorf("run_bash 的裸文件名输出被当成了路径证据（会把它们映射到错误的片）: %v", files)
		}
	}
}
