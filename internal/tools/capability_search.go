package tools

import (
	"context"
	"fmt"
)

// CapabilitySearcher 是 capability_search 工具需要的外部依赖接口
// 该接口可由 agents 包中的 CapabilityRegistry 适配器实现
type CapabilitySearcher interface {
	// Search 根据查询条件搜索匹配的能力/Agent
	// query 可以是字符串或结构化 map
	Search(query interface{}) ([]interface{}, error)
}

// NewCapabilitySearchAdapter 创建 capability_search 工具适配器
// searcher 参数是 CapabilityRegistry 的适配器（由调用方传入）
func NewCapabilitySearchAdapter(searcher CapabilitySearcher) *Adapter {
	// 工具描述（LLM 视角）
	desc := `Search for other agents that can help with a specific task. Returns a list of agents with their IDs, names, descriptions, and capability tags. ALWAYS call this before attempting a task that might be outside your core expertise.`

	adapter := NewAdapter("capability_search", desc, func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		queryText, _ := params["query"].(string)
		if queryText == "" {
			return nil, fmt.Errorf("capability_search: query is required")
		}

		if searcher == nil {
			return "Capability Registry not available (running in legacy mode)", nil
		}

		result, err := searcher.Search(queryText)
		if err != nil {
			return nil, fmt.Errorf("capability_search: search failed: %w", err)
		}

		// 格式化结果为易读文本
		if len(result) == 0 {
			return "No agents found matching your query. Try a different query or handle the task yourself.", nil
		}

		output := fmt.Sprintf("Found %d agent(s):\n", len(result))
		for i, r := range result {
			capMap, ok := r.(map[string]interface{})
			if !ok {
				continue
			}
			agentID, _ := capMap["agent_id"].(string)
			name, _ := capMap["name"].(string)
			agentDesc, _ := capMap["description"].(string)
			tagsRaw, _ := capMap["tags"].([]interface{})

			tags := make([]string, len(tagsRaw))
			for j, t := range tagsRaw {
				tags[j] = fmt.Sprintf("%v", t)
			}

			output += fmt.Sprintf("  %d. %s (id: %s) — %s\n", i+1, name, agentID, agentDesc)
			if len(tags) > 0 {
				output += fmt.Sprintf("     Tags: [%s]\n", joinStrings(tags, ", "))
			}
		}
		output += "\nUse p2p_delegate with the agent_id to request help."

		return output, nil
	})

	// 设置 JSON Schema
	adapter.schema = map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Natural language description of the task or skill you need help with",
			},
		},
		"required": []string{"query"},
	}

	return adapter
}

// CapabilitySearcherFunc 是 CapabilitySearcher 的函数适配器
// 允许使用普通函数实现 CapabilitySearcher 接口
type CapabilitySearcherFunc func(queryText string) ([]interface{}, error)

// Search 实现 CapabilitySearcher 接口
func (f CapabilitySearcherFunc) Search(query interface{}) ([]interface{}, error) {
	switch v := query.(type) {
	case string:
		return f(v)
	case map[string]interface{}:
		if text, ok := v["query"].(string); ok {
			return f(text)
		}
	}
	// 尝试将 query 转为字符串并搜索
	return f(fmt.Sprintf("%v", query))
}

// joinStrings 辅助函数：将字符串切片用分隔符连接
func joinStrings(strs []string, sep string) string {
	if len(strs) == 0 {
		return ""
	}
	result := strs[0]
	for _, s := range strs[1:] {
		result += sep + s
	}
	return result
}
