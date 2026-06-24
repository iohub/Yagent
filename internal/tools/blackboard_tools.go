package tools

import (
	"context"
	"fmt"
)

// BlackboardAccessor 是黑板的访问接口（由 agents 包实现）
type BlackboardAccessor interface {
	// Post 发布条目到指定区域，返回 entry ID
	Post(region string, author string, content map[string]interface{}, tags []string, references []string) (string, error)
	// Read 从指定区域读取条目
	Read(region string, filter map[string]interface{}) ([]map[string]interface{}, error)
	// Get 按 ID 获取条目
	Get(entryID string) (map[string]interface{}, bool)
}

// NewBlackboardReadAdapter 创建 blackboard_read 工具适配器
// LLM 看到的描述：
// "Read entries from the shared blackboard.
//  Regions:
//  - \"tasks\": task definitions and subtasks
//  - \"findings\": intermediate results and discoveries
//  - \"decisions\": decisions made by agents
//  - \"questions\": open questions waiting for answers
//  - \"artifacts\": final deliverables
//  ALWAYS check the blackboard before starting work."
func NewBlackboardReadAdapter(board BlackboardAccessor) *Adapter {
	desc := `Read entries from the shared blackboard where agents share information.
Regions:
- "tasks": task definitions and subtasks
- "findings": intermediate results and discoveries  
- "decisions": decisions made by agents
- "questions": open questions waiting for answers
- "artifacts": final deliverables
ALWAYS check the blackboard before starting work to avoid duplicating effort.`

	adapter := NewAdapter("blackboard_read", desc, func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		region, _ := params["region"].(string)
		if region == "" {
			return nil, fmt.Errorf("blackboard_read: region is required (tasks|findings|decisions|questions|artifacts)")
		}

		if board == nil {
			return "Blackboard not available (running in legacy mode)", nil
		}

		// 构建过滤器
		filter := make(map[string]interface{})
		if tags, ok := params["tags"].([]interface{}); ok {
			filter["tags"] = tags
		}
		if author, ok := params["author"].(string); ok {
			filter["author"] = author
		}
		if status, ok := params["status"].(string); ok {
			filter["status"] = status
		}

		entries, err := board.Read(region, filter)
		if err != nil {
			return nil, fmt.Errorf("blackboard_read: %w", err)
		}

		if len(entries) == 0 {
			return fmt.Sprintf("No entries found in region '%s'.", region), nil
		}

		// 格式化为易读文本
		output := fmt.Sprintf("Blackboard [%s] — %d entries:\n\n", region, len(entries))
		for i, entry := range entries {
			id, _ := entry["id"].(string)
			author, _ := entry["author"].(string)
			status, _ := entry["status"].(string)
			createdAt, _ := entry["created_at"].(string)
			content, _ := entry["content"].(map[string]interface{})

			output += fmt.Sprintf("[%d] ID: %s | Author: %s | Status: %s | Created: %s\n",
				i+1, id, author, status, createdAt)

			if content != nil {
				for k, v := range content {
					output += fmt.Sprintf("    %s: %v\n", k, v)
				}
			}
			output += "\n"
		}

		return output, nil
	})

	adapter.schema = map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"region": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"tasks", "findings", "decisions", "questions", "artifacts"},
				"description": "The blackboard region to read from",
			},
			"tags": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Optional: filter by tags (AND logic)",
			},
			"author": map[string]interface{}{
				"type":        "string",
				"description": "Optional: filter by author agent ID",
			},
			"status": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"draft", "committed", "superseded", "closed"},
				"description": "Optional: filter by status",
			},
		},
		"required": []string{"region"},
	}

	return adapter
}

// NewBlackboardPostAdapter 创建 blackboard_post 工具适配器
// LLM 看到的描述：
// "Post a finding, decision, or question to the shared blackboard.
//  Other agents will be able to read this and build on your work.
//  Be concise but complete — include enough context for another agent to understand."
func NewBlackboardPostAdapter(board BlackboardAccessor) *Adapter {
	desc := `Post a finding, decision, or question to the shared blackboard. Other agents will be able to read this and build on your work. Be concise but complete — include enough context for another agent to understand.`

	adapter := NewAdapter("blackboard_post", desc, func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		region, _ := params["region"].(string)
		if region == "" {
			return nil, fmt.Errorf("blackboard_post: region is required (tasks|findings|decisions|questions|artifacts)")
		}

		if board == nil {
			return "Blackboard not available (running in legacy mode)", nil
		}

		// 获取作者（从 params 或使用默认值）
		author, _ := params["author"].(string)
		if author == "" {
			author = "unknown"
		}

		// 构建结构化内容
		content := make(map[string]interface{})
		if summary, ok := params["summary"].(string); ok {
			content["summary"] = summary
		}
		if details, ok := params["details"].(string); ok {
			content["details"] = details
		}
		// 捕获所有其他字段进入 content
		for k, v := range params {
			if k != "region" && k != "author" && k != "tags" && k != "references" && k != "content" {
				if _, exists := content[k]; !exists {
					content[k] = v
				}
			}
		}
		// 如果有 content 参数，用它扩展
		if contentParam, ok := params["content"].(map[string]interface{}); ok {
			for k, v := range contentParam {
				content[k] = v
			}
		}

		// 获取标签
		tags := make([]string, 0)
		if tagsRaw, ok := params["tags"].([]interface{}); ok {
			for _, t := range tagsRaw {
				if s, ok := t.(string); ok {
					tags = append(tags, s)
				}
			}
		}

		// 获取引用
		refs := make([]string, 0)
		if refsRaw, ok := params["references"].([]interface{}); ok {
			for _, r := range refsRaw {
				if s, ok := r.(string); ok {
					refs = append(refs, s)
				}
			}
		}

		entryID, err := board.Post(region, author, content, tags, refs)
		if err != nil {
			return nil, fmt.Errorf("blackboard_post: %w", err)
		}

		return fmt.Sprintf("Posted to [%s] as entry %s. Other agents will see this when they check the blackboard.", region, entryID), nil
	})

	adapter.schema = map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"region": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"tasks", "findings", "decisions", "questions", "artifacts"},
				"description": "The blackboard region to post to",
			},
			"summary": map[string]interface{}{
				"type":        "string",
				"description": "Brief summary of the finding/decision/question",
			},
			"details": map[string]interface{}{
				"type":        "string",
				"description": "Detailed explanation or data",
			},
			"tags": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Tags for discoverability",
			},
			"references": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Entry IDs this post references",
			},
		},
		"required": []string{"region", "summary"},
	}

	return adapter
}
