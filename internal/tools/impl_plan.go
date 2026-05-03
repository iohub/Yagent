package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// ImplPlanTool maintains a stateful implementation plan document in memory.
// CodingAgent uses it to create and evolve detailed design documents
// for complex multi-step programming tasks. The plan lives only for the
// duration of the agent run — it does not persist to disk to avoid
// polluting the next task.
type ImplPlanTool struct {
	mu          sync.Mutex
	planContent string
}

// NewImplPlanTool creates a new ImplPlanTool with empty state.
func NewImplPlanTool() *ImplPlanTool {
	return &ImplPlanTool{}
}

// GetPlan returns the current plan content. Thread-safe.
func (t *ImplPlanTool) GetPlan() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.planContent
}

// Execute dispatches actions on the implementation plan.
// Implements the ToolFunc signature.
func (t *ImplPlanTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	action, _ := params["action"].(string)
	if action == "" {
		action = "get" // default: return current plan
	}

	switch action {
	case "create":
		planContent, _ := params["plan_content"].(string)
		if strings.TrimSpace(planContent) == "" {
			return map[string]interface{}{
				"success": false,
				"message": "plan_content is required for create action and must be non-empty",
			}, fmt.Errorf("plan_content is empty")
		}
		t.planContent = planContent
		return map[string]interface{}{
			"success": true,
			"message": "Implementation plan created. Use impl_plan with action=get to review it at any time. Use action=update when design flaws are discovered.",
			"plan":    t.planContent,
		}, nil

	case "update":
		section, _ := params["section"].(string)
		newContent, _ := params["new_content"].(string)
		if strings.TrimSpace(newContent) == "" {
			return map[string]interface{}{
				"success": false,
				"message": "new_content is required for update action",
			}, fmt.Errorf("new_content is empty")
		}

		if section != "" {
			t.planContent = replaceSection(t.planContent, section, newContent)
		} else {
			// Append as a revision block
			if t.planContent != "" {
				t.planContent += "\n\n---\n## " + sectionOrRevision(section) + "\n" + newContent
			} else {
				t.planContent = newContent
			}
		}
		return map[string]interface{}{
			"success": true,
			"message": "Implementation plan updated.",
			"plan":    t.planContent,
		}, nil

	case "get":
		if t.planContent == "" {
			return map[string]interface{}{
				"success": true,
				"message": "No implementation plan exists yet. Use action=create to create one.",
				"plan":    "",
			}, nil
		}
		return map[string]interface{}{
			"success": true,
			"message": "Current implementation plan retrieved.",
			"plan":    t.planContent,
		}, nil

	case "clear":
		t.planContent = ""
		return map[string]interface{}{
			"success": true,
			"message": "Implementation plan cleared.",
			"plan":    "",
		}, nil

	default:
		return nil, fmt.Errorf("unknown action: %s (valid: create, get, update, clear)", action)
	}
}

// replaceSection replaces a markdown section (## Section Name) in the plan document.
// If the section doesn't exist, it appends it at the end.
func replaceSection(doc, sectionName, newContent string) string {
	header := "## " + sectionName
	idx := strings.Index(doc, header)
	if idx < 0 {
		// Section not found: try case-insensitive
		lower := strings.ToLower(doc)
		lowerHeader := strings.ToLower(header)
		idx = strings.Index(lower, lowerHeader)
	}

	if idx < 0 {
		// Append new section
		if doc != "" {
			doc += "\n\n"
		}
		return doc + header + "\n" + newContent
	}

	// Find the end of this section (next ## header or end of doc)
	endIdx := strings.Index(doc[idx+len(header):], "\n## ")
	if endIdx >= 0 {
		endIdx += idx + len(header)
	} else {
		endIdx = len(doc)
	}

	// Replace section content
	return doc[:idx] + header + "\n" + newContent + doc[endIdx:]
}

func sectionOrRevision(s string) string {
	if s != "" {
		return s
	}
	return "Revision"
}
