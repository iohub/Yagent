package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"codeactor/internal/config"
)

// ─── Anthropic Messages API Types ────────────────────────────────────────────

// anthropicRequest is the request body for Anthropic Messages API.
type anthropicRequest struct {
	Model       string            `json:"model"`
	MaxTokens   int               `json:"max_tokens"`
	Temperature float64           `json:"temperature,omitempty"`
	System      string            `json:"system,omitempty"`
	Messages    []anthropicMsg    `json:"messages"`
	Tools       []anthropicTool   `json:"tools,omitempty"`
	Thinking    *anthropicThinking `json:"thinking,omitempty"`
	Stream      bool              `json:"stream,omitempty"`
}

type anthropicMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Data      string          `json:"data,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

// anthropicResponse is the response body from Anthropic Messages API.
type anthropicResponse struct {
	ID         string                  `json:"id"`
	Type       string                  `json:"type"`
	Role       string                  `json:"role"`
	Content    []anthropicContentBlock `json:"content"`
	Model      string                  `json:"model"`
	StopReason string                  `json:"stop_reason"`
	Usage      anthropicUsage          `json:"usage"`
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

type anthropicAPIError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type anthropicErrorBody struct {
	Type  string            `json:"type"`
	Error anthropicAPIError `json:"error"`
}

// ─── AnthropicEngine ─────────────────────────────────────────────────────────

// AnthropicEngine implements Engine using Anthropic's Messages API.
// It supports text generation, tool use, streaming, and extended thinking.
type AnthropicEngine struct {
	client          *http.Client
	transport       *http.Transport
	baseURL         string
	apiKey          string
	model           string
	maxTokens       int
	cfg             config.LLMConfig
	reasoningEffort string
}

// NewAnthropicEngine creates a new AnthropicEngine.
// baseURL is the Anthropic API base URL (e.g., "https://api.anthropic.com/v1").
// apiKey is the Anthropic API key.
// model is the model name (e.g., "claude-sonnet-4-20250514").
func NewAnthropicEngine(baseURL, apiKey, model string, cfg config.LLMConfig, reasoningEffort string) *AnthropicEngine {
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		MaxIdleConns:          10,
		MaxIdleConnsPerHost:   5,
		IdleConnTimeout:       90 * time.Second,
		DisableKeepAlives:     false,
	}

	httpClient := &http.Client{
		Transport: transport,
	}

	return &AnthropicEngine{
		client:          httpClient,
		transport:       transport,
		baseURL:         strings.TrimRight(baseURL, "/"),
		apiKey:          apiKey,
		model:           model,
		maxTokens:       8192, // default, overridden by CallOptions
		cfg:             cfg,
		reasoningEffort: reasoningEffort,
	}
}

// Model returns the model name.
func (e *AnthropicEngine) Model() string { return e.model }

// CloseIdleConnections closes idle HTTP connections.
func (e *AnthropicEngine) CloseIdleConnections() {
	if e.transport != nil {
		e.transport.CloseIdleConnections()
	}
}

// ─── Engine Interface ────────────────────────────────────────────────────────

// GenerateContent implements Engine.
func (e *AnthropicEngine) GenerateContent(ctx context.Context, messages []Message, tools []ToolDef, opts *CallOptions) (*Response, error) {
	if e.cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.cfg.Timeout)
		defer cancel()
	}

	req := e.buildRequestBody(messages, tools, opts)

	if opts != nil && opts.StreamHandler != nil {
		return e.streamGenerate(ctx, req, opts.StreamHandler)
	}

	return e.nonStreamGenerate(ctx, req)
}
