package assistant

import (
	"context"
	"fmt"
)

// ExecuteReturnResultWithSummary implements the return_result_with_summary tool
func (tm *EnhancedToolManager) ExecuteReturnResultWithSummary(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	result, ok := params["result"].(string)
	if !ok {
		return nil, fmt.Errorf("result parameter must be a string")
	}

	summary, ok := params["summary"].(string)
	if !ok {
		return nil, fmt.Errorf("summary parameter must be a string")
	}

	// This tool doesn't perform side effects - it signals completion with context
	// The orchestrator agent will capture this and propagate upward
	return map[string]interface{}{
		"tool":     "return_result_with_summary",
		"result":   result,
		"summary":  summary,
	}, nil
}