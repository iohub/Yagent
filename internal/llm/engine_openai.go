package llm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/shared"
)

// OpenAIEngine implements Engine using the official OpenAI Go SDK.
type OpenAIEngine struct {
	client *openai.Client
	model  string
}

// NewOpenAIEngine creates a new OpenAIEngine.
// baseURL is optional - if empty, uses OpenAI's default API endpoint.
func NewOpenAIEngine(baseURL, apiKey, model string) *OpenAIEngine {
	opts := []option.RequestOption{
		option.WithAPIKey(apiKey),
	}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	client := openai.NewClient(opts...)
	return &OpenAIEngine{client: &client, model: model}
}

// GenerateContent implements Engine.
func (e *OpenAIEngine) GenerateContent(ctx context.Context, messages []Message, tools []ToolDef, opts *CallOptions) (*Response, error) {
	params := e.buildParams(messages, tools, opts)

	if opts != nil && opts.StreamHandler != nil {
		return e.generateStreaming(ctx, params, opts.StreamHandler)
	}

	completion, err := e.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("openai chat completion: %w", err)
	}

	return e.toResponse(completion), nil
}

func (e *OpenAIEngine) buildParams(messages []Message, tools []ToolDef, opts *CallOptions) openai.ChatCompletionNewParams {
	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(e.model),
		Messages: e.convertMessages(messages),
	}

	if len(tools) > 0 {
		params.Tools = e.convertTools(tools)
	}

	if opts != nil {
		if opts.MaxTokens > 0 {
			params.MaxCompletionTokens = openai.Int(int64(opts.MaxTokens))
		}
		if opts.Temperature > 0 {
			params.Temperature = openai.Float(opts.Temperature)
		}
	}

	return params
}

func (e *OpenAIEngine) generateStreaming(ctx context.Context, params openai.ChatCompletionNewParams, handler StreamHandler) (*Response, error) {
	stream := e.client.Chat.Completions.NewStreaming(ctx, params)
	defer stream.Close()

	var content string
	var reasoning string
	var toolCalls []ToolCall

	for stream.Next() {
		chunk := stream.Current()
		if len(chunk.Choices) > 0 {
			delta := chunk.Choices[0].Delta
			content += delta.Content

			// Extract reasoning_content from raw JSON if present (DeepSeek thinking mode)
			if raw := delta.RawJSON(); raw != "" {
				var rawDelta map[string]any
				if err := json.Unmarshal([]byte(raw), &rawDelta); err == nil {
					if rc, ok := rawDelta["reasoning_content"].(string); ok {
						reasoning += rc
					}
				}
			}

			// Accumulate tool call deltas
			for _, tc := range delta.ToolCalls {
				idx := int(tc.Index)
				if idx >= len(toolCalls) {
					// Extend slice if needed
					for len(toolCalls) <= idx {
						toolCalls = append(toolCalls, ToolCall{Type: "function"})
					}
				}

				if tc.ID != "" {
					toolCalls[idx].ID = tc.ID
				}
				if tc.Function.Name != "" {
					toolCalls[idx].Function.Name = tc.Function.Name
				}
				toolCalls[idx].Function.Arguments += tc.Function.Arguments
			}

			// Call handler with content chunk
			if handler != nil && delta.Content != "" {
				if err := handler(ctx, []byte(delta.Content)); err != nil {
					return nil, err
				}
			}
		}
	}

	if err := stream.Err(); err != nil {
		return nil, fmt.Errorf("openai streaming: %w", err)
	}

	return &Response{
		Choices: []Choice{{
			Content:   content,
			Reasoning: reasoning,
			ToolCalls: toolCalls,
		}},
	}, nil
}

func (e *OpenAIEngine) convertMessages(messages []Message) []openai.ChatCompletionMessageParamUnion {
	result := make([]openai.ChatCompletionMessageParamUnion, 0, len(messages))
	for _, msg := range messages {
		result = append(result, e.convertMessage(msg))
	}
	return result
}

func (e *OpenAIEngine) convertMessage(msg Message) openai.ChatCompletionMessageParamUnion {
	switch msg.Role {
	case RoleSystem:
		return openai.SystemMessage(msg.Content)
	case RoleUser:
		return openai.UserMessage(msg.Content)
	case RoleAssistant:
		if len(msg.ToolCalls) > 0 {
			return e.buildAssistantWithToolCalls(msg)
		}
		content := msg.Content
		if msg.Reasoning != "" {
			// DeepSeek thinking mode: reasoning_content must be echoed back.
			// openai-go types don't expose reasoning_content, so inject via SetExtraFields.
			assistant := &openai.ChatCompletionAssistantMessageParam{
				Content: openai.ChatCompletionAssistantMessageParamContentUnion{
					OfString: param.NewOpt(content),
				},
			}
			assistant.SetExtraFields(map[string]any{
				"reasoning_content": msg.Reasoning,
			})
			return openai.ChatCompletionMessageParamUnion{
				OfAssistant: assistant,
			}
		}
		return openai.AssistantMessage(content)
	case RoleTool:
		return openai.ToolMessage(msg.Content, msg.ToolCallID)
	default:
		return openai.UserMessage(msg.Content)
	}
}

func (e *OpenAIEngine) buildAssistantWithToolCalls(msg Message) openai.ChatCompletionMessageParamUnion {
	toolCalls := make([]openai.ChatCompletionMessageToolCallUnionParam, 0, len(msg.ToolCalls))
	for _, tc := range msg.ToolCalls {
		toolCalls = append(toolCalls, openai.ChatCompletionMessageToolCallUnionParam{
			OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
				ID:   tc.ID,
				Type: "function",
				Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			},
		})
	}

	contentVal := msg.Content
	assistant := &openai.ChatCompletionAssistantMessageParam{
		Content: openai.ChatCompletionAssistantMessageParamContentUnion{
			OfString: param.NewOpt(contentVal),
		},
		ToolCalls: toolCalls,
	}
	if msg.Reasoning != "" {
		assistant.SetExtraFields(map[string]any{
			"reasoning_content": msg.Reasoning,
		})
	}
	return openai.ChatCompletionMessageParamUnion{
		OfAssistant: assistant,
	}
}

func (e *OpenAIEngine) convertTools(tools []ToolDef) []openai.ChatCompletionToolUnionParam {
	result := make([]openai.ChatCompletionToolUnionParam, 0, len(tools))
	for _, t := range tools {
		if t.Type == "function" {
			result = append(result, openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
				Name:        t.Function.Name,
				Description: openai.String(t.Function.Description),
				Parameters:  shared.FunctionParameters(t.Function.Parameters),
			}))
		}
	}
	return result
}

func (e *OpenAIEngine) toResponse(completion *openai.ChatCompletion) *Response {
	resp := &Response{}
	for _, choice := range completion.Choices {
		c := Choice{
			Content: choice.Message.Content,
		}

		// Extract tool calls
		for _, tc := range choice.Message.ToolCalls {
			fn := tc.AsFunction()
			if fn.ID != "" {
				c.ToolCalls = append(c.ToolCalls, ToolCall{
					ID:   fn.ID,
					Type: "function",
					Function: FunctionCall{
						Name:      fn.Function.Name,
						Arguments: fn.Function.Arguments,
					},
				})
			}
		}

		// Extract reasoning_content from raw JSON if present
		if raw := choice.Message.RawJSON(); raw != "" {
			var rawMsg map[string]any
			if err := json.Unmarshal([]byte(raw), &rawMsg); err == nil {
				if rc, ok := rawMsg["reasoning_content"].(string); ok {
					c.Reasoning = rc
				}
			}
		}

		resp.Choices = append(resp.Choices, c)
	}
	return resp
}
