package tools

import (
	"context"
	"strings"
	"testing"
)

// executor 是 ConsolidateKnowledgeTool / PruneHistoryTool 的公共形状，
// 让"nil MCP → skipped"这类同一契约的两个实现共用一张表。
type executor interface {
	Execute(context.Context, map[string]interface{}) (interface{}, error)
}

// ============================================================================
// extractJSON 测试
// ============================================================================

func TestExtractJSON(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"pure JSON", `{"title":"test","content":"hello"}`, `{"title":"test","content":"hello"}`},
		{"markdown fence with lang", "```json\n{\"title\":\"test\"}\n```", `{"title":"test"}`},
		{"markdown fence no lang", "```\n{\"key\":\"val\"}\n```", `{"key":"val"}`},
		{"no json", "just some plain text with no json", "just some plain text with no json"},
		{"surrounding text", `Here is the result: {"key": "value"} and more.`, `{"key": "value"}`},
		{"empty string", "", ""},
		{"missing closing brace", `{"key": "value"`, `{"key": "value"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractJSON(tc.input); got != tc.want {
				t.Errorf("extractJSON(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// ============================================================================
// ConsolidateKnowledgeTool.Execute 参数校验（未绑定模式）
// ============================================================================

func TestConsolidateKnowledgeTool_Execute_InvalidParams(t *testing.T) {
	cases := []struct {
		name       string
		params     map[string]interface{}
		wantErrSub string
	}{
		{
			name: "invalid type",
			params: map[string]interface{}{
				"type": "invalid_type", "title": "test", "content": "content",
				"tags": []interface{}{"tag1"}, "source_agent": "repo_agent",
			},
			wantErrSub: "invalid type",
		},
		{
			name: "missing title",
			params: map[string]interface{}{
				"type": "repo_retrieval", "content": "content",
				"tags": []interface{}{"tag1"}, "source_agent": "repo_agent",
			},
			wantErrSub: "title parameter is required",
		},
		{
			name: "missing content",
			params: map[string]interface{}{
				"type": "repo_retrieval", "title": "test",
				"tags": []interface{}{"tag1"}, "source_agent": "repo_agent",
			},
			wantErrSub: "content parameter is required",
		},
		{
			name: "missing tags",
			params: map[string]interface{}{
				"type": "repo_retrieval", "title": "test", "content": "content",
				"source_agent": "repo_agent",
			},
			wantErrSub: "tags parameter is required",
		},
		{
			name: "empty tags",
			params: map[string]interface{}{
				"type": "repo_retrieval", "title": "test", "content": "content",
				"tags": []interface{}{}, "source_agent": "repo_agent",
			},
			wantErrSub: "tags parameter is required",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool := NewConsolidateKnowledgeTool(nil, nil, "", "")
			_, err := tool.Execute(context.Background(), tc.params)
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
			// 前缀匹配而不是全等：措辞可以改，但"缺哪个参数"必须仍写在消息里。
			if !strings.Contains(err.Error(), tc.wantErrSub) {
				t.Errorf("error = %q, want it to mention %q", err.Error(), tc.wantErrSub)
			}
		})
	}
}

// ============================================================================
// nil MCP / codeseek 不可用 → fail-safe 返回 skipped
// ============================================================================

func TestKnowledgeTools_NilMCPReturnsSkipped(t *testing.T) {
	cases := []struct {
		name   string
		tool   executor
		params map[string]interface{}
	}{
		{
			// 绑定模式：params 里的非法 type/空 source_agent 应被绑定值覆盖
			name:   "consolidate/bound overrides invalid params",
			tool:   NewConsolidateKnowledgeTool(nil, nil, "repo_agent", "repo_retrieval"),
			params: map[string]interface{}{"type": "invalid_type", "source_agent": "", "title": "test title", "content": "test content here", "tags": []interface{}{"tag1"}},
		},
		{
			name:   "prune/list",
			tool:   NewPruneHistoryTool(nil, nil),
			params: map[string]interface{}{"action": "list"},
		},
		{
			name:   "prune/delete",
			tool:   NewPruneHistoryTool(nil, nil),
			params: map[string]interface{}{"action": "delete", "ids": []interface{}{"id1", "id2"}},
		},
		{
			name:   "prune/merge",
			tool:   NewPruneHistoryTool(nil, nil),
			params: map[string]interface{}{"action": "merge"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tc.tool.Execute(context.Background(), tc.params)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			resMap, ok := result.(map[string]interface{})
			if !ok {
				t.Fatalf("expected map result, got %T", result)
			}
			if resMap["status"] != "skipped" {
				t.Errorf("expected status 'skipped', got %v", resMap["status"])
			}
			if reason, _ := resMap["reason"].(string); reason == "" {
				t.Error("skipped result must carry a reason so the model knows why nothing was written")
			}
		})
	}
}

// 错误消息的前缀契约：消息里出现别的关键词就说明校验分支换了。
const (
	errPrefixSourceAgent = "source_agent"
	errPrefixPruneAction = "action must be one of"
)

// TestConsolidateKnowledgeTool_Execute_MissingSourceAgent 验证未绑定且 params 缺 source_agent 时报错
func TestConsolidateKnowledgeTool_Execute_MissingSourceAgent(t *testing.T) {
	tool := NewConsolidateKnowledgeTool(nil, nil, "", "")
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"type":    "repo_retrieval",
		"title":   "test",
		"content": "content",
		"tags":    []interface{}{"tag1"},
		// 未传 source_agent
	})
	if err == nil {
		t.Fatal("expected error for missing source_agent, got nil")
	}
	if !strings.HasPrefix(err.Error(), errPrefixSourceAgent) {
		t.Errorf("error = %q, want prefix %q", err.Error(), errPrefixSourceAgent)
	}
}

func TestPruneHistoryTool_Execute_InvalidAction(t *testing.T) {
	tool := NewPruneHistoryTool(nil, nil)
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "invalid_action",
	})
	if err == nil {
		t.Fatal("expected error for invalid action, got nil")
	}
	if !strings.HasPrefix(err.Error(), errPrefixPruneAction) {
		t.Errorf("error = %q, want prefix %q", err.Error(), errPrefixPruneAction)
	}
}

// ============================================================================
// mergeStrings 测试
// ============================================================================

func TestMergeStrings(t *testing.T) {
	cases := []struct {
		name string
		a, b []string
		want []string
	}{
		{"dedups and keeps order", []string{"tag1", "tag2"}, []string{"tag2", "tag3"}, []string{"tag1", "tag2", "tag3"}},
		{"both empty", nil, nil, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeStrings(tc.a, tc.b)
			if len(got) != len(tc.want) {
				t.Fatalf("mergeStrings(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("mergeStrings[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// ============================================================================
// replaceProjectAbsPath 测试
// ============================================================================

func TestReplaceProjectAbsPath(t *testing.T) {
	const projectDir = "/home/do/ssd/iohub/dev/yagent"

	cases := []struct {
		name       string
		title      string
		projectDir string
		want       string
	}{
		{"absolute prefix", projectDir + "/foo/bar.go 相关设计", projectDir, "foo/bar.go 相关设计"},
		{"no absolute path", "foo/bar.go 相关设计", projectDir, "foo/bar.go 相关设计"},
		{"exact match", projectDir, projectDir, "."},
		{"empty project dir", "/home/do/ssd/iohub/dev/yagent/foo/bar.go", "", "/home/do/ssd/iohub/dev/yagent/foo/bar.go"},
		{"empty title", "", projectDir, ""},
		{"no false prefix match", projectDir + "Xxx/foo/bar.go", projectDir, projectDir + "Xxx/foo/bar.go"},
		{"only rel path part", projectDir + "/", projectDir, "."},
		{"path in middle", "修复 " + projectDir + "/foo/bar.go 中的 bug", projectDir, "修复 foo/bar.go 中的 bug"},
		{"multiple paths",
			"参考 " + projectDir + "/a/x.go 和 " + projectDir + "/b/y.go 的改动", projectDir,
			"参考 a/x.go 和 b/y.go 的改动"},
		{"project dir alone", "参考 " + projectDir + " 文档", projectDir, "参考 . 文档"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := replaceProjectAbsPath(tc.title, tc.projectDir); got != tc.want {
				t.Errorf("replaceProjectAbsPath(%q, %q) = %q, want %q", tc.title, tc.projectDir, got, tc.want)
			}
		})
	}
}
