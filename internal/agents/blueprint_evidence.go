package agents

import (
	"encoding/json"
	"regexp"
	"strings"

	"yagent/internal/blueprint"
	"yagent/internal/llm"
	"yagent/internal/toolspec"
)

// 本文件是设计稿 §7.5 第 2 条的采集端：一条提交能覆盖哪些模块，
// 由"这次 run 真打开过哪些文件"决定，而不是由模型说了什么决定。
//
// 入参侧与**出参侧都要算**（v3 的理由照抄）：只认入参，一轮"检索到 8 个文件、
// 读过其中 3 个"的分析就只能提交 3 片，而那 8 片才是它真正掌握的。
// 只做入参侧的校验是个会误伤的校验 —— 闸门 TestEvidenceSetIncludesSearchResults。
//
// 这条挡不住"读了但说错"，但挡得住"没读就说"，而后者频率高得多。

// pathArgKeys 是工具参数里可能出现路径的键名。
// 判据是"键名"而不是"值长得像路径"：后者会把 commit message、symbol 名、
// 甚至一句英文里的 `a/b` 都收进证据集，那等于没有校验。
var pathArgKeys = map[string]bool{
	"path": true, "file_path": true, "filepath": true, "filename": true, "file": true,
	"old_path": true, "new_path": true, "dir": true, "directory": true, "target_file": true,
	"root_path": true, "paths": true, "files": true,
}

// searchHitKeys 是出参侧（检索类工具的结果）里代表"命中文件"的键。
var searchHitKeys = map[string]bool{
	"file_path": true, "filepath": true, "path": true, "filename": true, "file": true,
}

// rgLineRe 认 `path/to/file.go:12:` 这种 ripgrep 命中行（search_by_regex 的既有形态）。
var rgLineRe = regexp.MustCompile(`(?m)^\s*(\S+\.\w{1,6}):(\d+):`)

// collectBlueprintSubmission 取回本次 run 的蓝图提交与它的证据。
//
// 证据只在**真有提交时**才计算：绝大多数委派不会提交蓝图，
// 为它们扫一遍 History 是把 R6 的成本摊到每一轮上。
func collectBlueprintSubmission(exec ExecutorResult) ([]blueprint.FoldItem, []string) {
	items := parseBlueprintParam(exec.Text)
	if len(items) == 0 {
		return nil, nil
	}
	return items, evidenceFiles(exec.History)
}

// parseBlueprintParam 从 agent_exit 的返回值里读 `blueprint[]`。
// 采集点只有这一处（与 collectHandoff 同一个入口）：executor 不认识蓝图，
// 工具层只回显参数 —— 于是 rollout / TUI / 这里看到的是同一份 JSON。
func parseBlueprintParam(text string) []blueprint.FoldItem {
	if !isAgentExitResult(text) {
		return nil
	}
	var probe struct {
		Blueprint []struct {
			Section    string   `json:"section"`
			Module     string   `json:"module"`
			Field      string   `json:"field"`
			Claim      string   `json:"claim"`
			Where      string   `json:"where"`
			Supersedes []string `json:"supersedes"`
		} `json:"blueprint"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &probe); err != nil {
		return nil
	}
	var out []blueprint.FoldItem
	for _, raw := range probe.Blueprint {
		out = append(out, blueprint.FoldItem{
			Section: raw.Section, Module: raw.Module, Field: raw.Field,
			Claim: raw.Claim, Where: raw.Where, Supersedes: raw.Supersedes,
		})
	}
	return out
}

// evidenceFiles 把 History 里出现过的文件路径收成一列（未去重、未映射到模块 ——
// 映射由 Snapshot.EvidenceModules 做，它才知道边界）。
func evidenceFiles(history []llm.Message) []string {
	var out []string
	for i := range history {
		m := &history[i]
		for _, tc := range m.ToolCalls {
			out = append(out, pathsFromJSON([]byte(tc.Function.Arguments), pathArgKeys)...)
		}
		if m.Role != llm.RoleTool || m.Content == "" {
			continue
		}
		// 出参侧的形态问注册表，不 switch 工具名（check_no_manual_lookup.sh 守这条）：
		// "这个工具的结果里路径长什么样"是工具自己的事实，写在它自己的 Spec 上。
		switch toolspec.ResultPathsOf(m.ToolName) {
		case toolspec.ResultPathsBare:
			// list_dir / print_dir_tree 的输出是**裸文件名**（没有目录前缀）。
			// 收进来会把它们映射到错误的片 —— 宁可少一块证据，也不要一条假证据。
		case toolspec.ResultPathsRipgrep:
			out = append(out, pathsFromRipgrep(m.Content)...)
		default:
			out = append(out, pathsFromJSON([]byte(m.Content), searchHitKeys)...)
		}
	}
	return out
}

// pathsFromJSON 解一段 JSON，按**键名**收集字符串值（含数组与嵌套对象）。
// 解析失败返回 nil：证据侧的容错方向永远是"少收"，不是"多收"。
func pathsFromJSON(data []byte, keys map[string]bool) []string {
	if len(data) == 0 {
		return nil
	}
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return nil
	}
	var out []string
	walkJSON(v, keys, "", &out)
	return out
}

func walkJSON(v interface{}, keys map[string]bool, key string, out *[]string) {
	switch t := v.(type) {
	case map[string]interface{}:
		for k, val := range t {
			walkJSON(val, keys, k, out)
		}
	case []interface{}:
		for _, val := range t {
			walkJSON(val, keys, key, out)
		}
	case string:
		if keys[key] && strings.TrimSpace(t) != "" {
			*out = append(*out, t)
		}
	}
}

// pathsFromRipgrep 从 `path.go:12: text` 形式的命中行里取路径列。
func pathsFromRipgrep(content string) []string {
	var out []string
	for _, m := range rgLineRe.FindAllStringSubmatch(content, -1) {
		out = append(out, m[1])
	}
	return out
}
