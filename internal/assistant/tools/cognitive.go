package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// ThinkingTool allows the agent to reflect on errors and plan corrections.
type ThinkingTool struct{}

func NewThinkingTool() *ThinkingTool {
	return &ThinkingTool{}
}

func (t *ThinkingTool) Name() string {
	return "thinking"
}

func (t *ThinkingTool) Call(ctx context.Context, input string) (string, error) {
	var params struct {
		ErrorMessage  string `json:"error_message"`
		CurrentAction string `json:"current_action"`
		Observation   string `json:"observation"`
	}

	if err := json.Unmarshal([]byte(input), &params); err != nil {
		// Try to handle if input is just a string description
		params.ErrorMessage = input
	}

	return fmt.Sprintf("Thinking Process Logged:\nError: %s\nAction: %s\nObservation: %s\n\nAnalysis: Please analyze the above error and propose a fix.",
		params.ErrorMessage, params.CurrentAction, params.Observation), nil
}
