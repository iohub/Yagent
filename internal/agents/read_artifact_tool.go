package agents

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"

	"yagent/internal/agents/director"
	"yagent/internal/artifact"
	"yagent/internal/tools"
)

// 本文件实现"委派返回物分级"第三批次：read_artifact 工具。
// 委派结果超预算被截断落盘为 artifact 后，Director 通过 [Sub-Agent Result]
// 头部的 artifact: 字段拿到 ID，需要细节时按 ID 分页回读全文。
// 错误一律以友好文本返回给 LLM（便于自我纠正），不返回 Go error。

const (
	// readArtifactDefaultMaxLines 单次返回的默认行数。
	readArtifactDefaultMaxLines = 200
	// readArtifactHardMaxLines 单次返回的硬上限（超出钳制到该值）。
	readArtifactHardMaxLines = 500
	// readArtifactMaxLineRunes 单行展示的 rune 上限（超出截断并标注）。
	readArtifactMaxLineRunes = 4000
)

// newReadArtifactAdapter 构造 read_artifact 工具适配器（注册进 Director 工具集）。
func newReadArtifactAdapter() *tools.Adapter {
	return tools.NewAdapter("read_artifact",
		"Read the full artifact content of a delegated sub-agent result, paginated by line. "+
			"Use when the [Sub-Agent Result] header shows an 'artifact: <id>' field and you need details "+
			"beyond the truncated summary. Params: id (required) — the artifact ID from the [Sub-Agent Result] "+
			"header; start_line (optional, 1-based, default 1); max_lines (optional, default 200, hard cap 500).",
		executeReadArtifact).WithSchema(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"id": map[string]interface{}{
				"type":        "string",
				"description": "Artifact ID from the [Sub-Agent Result] header (artifact: field).",
			},
			"start_line": map[string]interface{}{
				"type":        "integer",
				"description": "1-based start line (default 1).",
			},
			"max_lines": map[string]interface{}{
				"type":        "integer",
				"description": "Max lines to return (default 200, hard cap 500).",
			},
		},
		"required": []string{"id"},
	})
}

// executeReadArtifact read_artifact 工具 handler。
func executeReadArtifact(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	// 1. 参数解析与校验。
	id, _ := params["id"].(string)
	id = strings.TrimSpace(id)
	if id == "" {
		return readArtifactErrorText("invalid artifact id: (empty)"), nil
	}

	startLine := intParam(params, "start_line", 1)
	if startLine < 1 {
		startLine = 1
	}
	maxLines := intParam(params, "max_lines", readArtifactDefaultMaxLines)
	if maxLines < 1 {
		maxLines = readArtifactDefaultMaxLines
	}
	if maxLines > readArtifactHardMaxLines {
		maxLines = readArtifactHardMaxLines
	}

	// 2. projectID：与 FinalizeResult 落盘时的计算方式同源（同一 projectPathProvider）。
	projectID := director.ComputeProjectID(currentProjectPath())

	// 3. Load（而非 LoadFullText）：需 Agent 等元信息填充头部展示行。
	art, err := artifact.DefaultStore().Load(projectID, id)
	if err != nil {
		msg := err.Error()
		switch {
		case strings.Contains(msg, "invalid artifact id"):
			// artifact 包 ID 白名单校验失败（含路径穿越尝试）。
			return readArtifactErrorText("invalid artifact id: " + id), nil
		case errors.Is(err, fs.ErrNotExist):
			// Load 用 %w 包装 os.ReadFile 错误，errors.Is 可穿透检测 not-found。
			return readArtifactErrorText("artifact not found: " + id), nil
		default:
			return readArtifactErrorText(fmt.Sprintf("read_artifact failed: %v", err)), nil
		}
	}

	// 4. 按 \n 切行（保留行结构）+ 应用 start_line/max_lines 分页。
	lines := strings.Split(art.FullText, "\n")
	total := len(lines)

	if startLine > total {
		// start_line 越界：空正文 + 友好提示，不 panic。
		var b strings.Builder
		fmt.Fprintf(&b, "artifact %s | agent: %s | %d lines | showing nothing\n", art.ID, art.Agent, total)
		fmt.Fprintf(&b, "start_line=%d exceeds total lines (%d). Retry with a start_line between 1 and %d.",
			startLine, total, total)
		return b.String(), nil
	}

	end := startLine - 1 + maxLines
	if end > total {
		end = total
	}

	// 行号宽度按总行数右对齐（read_file 风格）。
	width := len(strconv.Itoa(total))

	var b strings.Builder
	fmt.Fprintf(&b, "artifact %s | agent: %s | %d lines | showing %d-%d\n", art.ID, art.Agent, total, startLine, end)
	for i, line := range lines[startLine-1 : end] {
		n := startLine + i
		if runes := []rune(line); len(runes) > readArtifactMaxLineRunes {
			// 长单行兜底：仅展示首块（4000 rune）并标注，防止单次返回爆量。
			fmt.Fprintf(&b, "%*d: %s ... [line %d truncated, %d runes total]\n",
				width, n, string(runes[:readArtifactMaxLineRunes]), n, len(runes))
			continue
		}
		fmt.Fprintf(&b, "%*d: %s\n", width, n, line)
	}
	if remaining := total - end; remaining > 0 {
		fmt.Fprintf(&b, "[more: %d lines left; continue with start_line=%d]", remaining, end+1)
	}
	return b.String(), nil
}

// intParam 从 params 取整型参数：LLM 经 JSON 调用时数字反序列化为 float64；
// 兼容 int 直传（程序化调用路径），其余类型回退默认值。
func intParam(params map[string]interface{}, key string, def int) int {
	switch v := params[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return def
	}
}

// readArtifactErrorText 错误文本统一格式：错误说明 + 自我纠正提示。
func readArtifactErrorText(msg string) string {
	return msg + "\n请核对委派结果头部中的 artifact id（[Sub-Agent Result] 头部的 artifact: 字段）。"
}
