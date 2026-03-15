// Package anthropic provides a streaming Anthropic Messages API client that
// implements pigeon's StreamingClient and modelCatalog interfaces.
//
// Supports both API-key auth (x-api-key header) and OAuth Bearer tokens
// (sk-ant-oat… tokens obtained via the Claude Pro/Max subscription flow).
// When an OAuth token is detected, the client automatically injects the
// required beta headers and identity system prompt so that the subscription
// quota is billed correctly.
package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"pigeon/internal/provider/openrouter"
)

const (
	apiBase          = "https://api.anthropic.com"
	messagesEndpoint = apiBase + "/v1/messages"
	anthropicVersion = "2023-06-01"
	defaultMaxTokens = 8192

	// oauthIdentityPrompt must be the first system block for OAuth tokens so
	// that Anthropic's backend recognises the Claude Code session and bills
	// the subscription correctly.
	oauthIdentityPrompt = "You are Claude Code, Anthropic's official CLI for Claude."

	// Beta feature headers.
	oauthBetaHeader  = "claude-code-20250219,oauth-2025-04-20,fine-grained-tool-streaming-2025-05-14"
	apiKeyBetaHeader = "fine-grained-tool-streaming-2025-05-14"

	// User-agent spoofed to match Claude Code so OAuth quota applies.
	oauthUserAgent = "claude-cli/2.1.75"
)

// staticModels is the curated list of Anthropic models exposed in pigeon's
// model picker. Update as new models ship.
var staticModels = []openrouter.ModelInfo{
	{ID: "claude-sonnet-4-6", Name: "Claude Sonnet 4.6", Provider: "anthropic"},
	{ID: "claude-opus-4-5", Name: "Claude Opus 4.5", Provider: "anthropic"},
	{ID: "claude-sonnet-4-5", Name: "Claude Sonnet 4.5", Provider: "anthropic"},
	{ID: "claude-haiku-4-5-20251001", Name: "Claude Haiku 4.5", Provider: "anthropic"},
	{ID: "claude-3-7-sonnet-20250219", Name: "Claude Sonnet 3.7", Provider: "anthropic"},
	{ID: "claude-3-5-sonnet-20241022", Name: "Claude Sonnet 3.5 v2", Provider: "anthropic"},
}

// isOAuthToken returns true for OAuth access tokens (sk-ant-oat…).
func isOAuthToken(key string) bool {
	return strings.Contains(key, "sk-ant-oat")
}

// Client is an Anthropic API client implementing pigeon's StreamingClient and
// modelCatalog interfaces. It is safe for concurrent use.
type Client struct {
	httpClient *http.Client
	apiKey     string
	isOAuth    bool
}

// NewClient creates a new Anthropic client. Pass nil for httpClient to use the
// default http.Client. The apiKey may be a regular API key (sk-ant-api…) or an
// OAuth access token (sk-ant-oat…).
func NewClient(apiKey string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		httpClient: httpClient,
		apiKey:     apiKey,
		isOAuth:    isOAuthToken(apiKey),
	}
}

// ListModels returns a static list of current Anthropic models.
func (c *Client) ListModels(_ context.Context) ([]openrouter.ModelInfo, error) {
	out := make([]openrouter.ModelInfo, len(staticModels))
	copy(out, staticModels)
	return out, nil
}

// StreamChatCompletion streams a chat completion from the Anthropic Messages
// API. It converts OpenAI-compat messages / tool definitions to Anthropic
// format on the way in and converts the streaming response back out.
func (c *Client) StreamChatCompletion(
	ctx context.Context,
	model string,
	messages []openrouter.Message,
	tools []openrouter.ToolDefinition,
	onEvent openrouter.StreamHandler,
) (openrouter.Message, error) {
	if model == "" {
		return openrouter.Message{}, fmt.Errorf("model is required")
	}
	if onEvent == nil {
		return openrouter.Message{}, fmt.Errorf("stream handler is required")
	}

	systemField, converted := toAnthropicMessages(messages, c.isOAuth)

	req := anthropicRequest{
		Model:     model,
		MaxTokens: defaultMaxTokens,
		System:    systemField,
		Messages:  converted,
		Stream:    true,
	}
	if len(tools) > 0 {
		req.Tools = toAnthropicTools(tools)
	}

	body, err := json.Marshal(req)
	if err != nil {
		return openrouter.Message{}, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, messagesEndpoint, bytes.NewReader(body))
	if err != nil {
		return openrouter.Message{}, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", anthropicVersion)

	if c.isOAuth {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
		httpReq.Header.Set("anthropic-beta", oauthBetaHeader)
		httpReq.Header.Set("User-Agent", oauthUserAgent)
		httpReq.Header.Set("x-app", "cli")
	} else {
		httpReq.Header.Set("x-api-key", c.apiKey)
		httpReq.Header.Set("anthropic-beta", apiKeyBetaHeader)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return openrouter.Message{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return openrouter.Message{}, parseHTTPError(resp)
	}

	return parseStream(resp.Body, onEvent)
}

// ─── Request / response types ───────────────────────────────────────────────

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    any                `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
	Stream    bool               `json:"stream"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string or []anthropicBlock
}

// anthropicBlock represents one element in a content array.
type anthropicBlock struct {
	Type string `json:"type"`

	// type == "text"
	Text string `json:"text,omitempty"`

	// type == "tool_use"
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// type == "tool_result"
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

// ─── Message conversion ─────────────────────────────────────────────────────

// toAnthropicMessages converts an OpenAI-compat message slice to Anthropic
// format.  System messages are extracted into a system field (injecting the
// OAuth identity prompt first when oauth==true).  Consecutive "tool" role
// messages are batched into a single user message with tool_result blocks.
func toAnthropicMessages(messages []openrouter.Message, oauth bool) (systemField any, converted []anthropicMessage) {
	var systemBlocks []map[string]any

	if oauth {
		systemBlocks = append(systemBlocks, map[string]any{
			"type": "text",
			"text": oauthIdentityPrompt,
		})
	}

	i := 0
	for i < len(messages) {
		msg := messages[i]
		switch msg.Role {
		case "system":
			systemBlocks = append(systemBlocks, map[string]any{
				"type": "text",
				"text": msg.Content,
			})
			i++

		case "user":
			converted = append(converted, anthropicMessage{
				Role:    "user",
				Content: msg.Content,
			})
			i++

		case "assistant":
			blocks := buildAssistantBlocks(msg)
			if len(blocks) > 0 {
				converted = append(converted, anthropicMessage{
					Role:    "assistant",
					Content: blocks,
				})
			}
			i++

		case "tool":
			// Batch consecutive tool messages into one user message.
			var results []anthropicBlock
			for i < len(messages) && messages[i].Role == "tool" {
				m := messages[i]
				results = append(results, anthropicBlock{
					Type:      "tool_result",
					ToolUseID: m.ToolCallID,
					Content:   m.Content,
				})
				i++
			}
			if len(results) > 0 {
				converted = append(converted, anthropicMessage{
					Role:    "user",
					Content: results,
				})
			}

		default:
			i++
		}
	}

	// Build system field.
	switch len(systemBlocks) {
	case 0:
		// no system
	case 1:
		if !oauth {
			// For API-key auth a plain string is fine.
			systemField = systemBlocks[0]["text"]
		} else {
			systemField = systemBlocks
		}
	default:
		systemField = systemBlocks
	}

	return systemField, converted
}

// buildAssistantBlocks converts an OpenAI-compat assistant message to a slice
// of Anthropic content blocks.
func buildAssistantBlocks(msg openrouter.Message) []anthropicBlock {
	var blocks []anthropicBlock
	if msg.Content != "" {
		blocks = append(blocks, anthropicBlock{Type: "text", Text: msg.Content})
	}
	for _, tc := range msg.ToolCalls {
		input := json.RawMessage("{}")
		if tc.Function.Arguments != "" {
			input = json.RawMessage(tc.Function.Arguments)
		}
		blocks = append(blocks, anthropicBlock{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: input,
		})
	}
	return blocks
}

func toAnthropicTools(tools []openrouter.ToolDefinition) []anthropicTool {
	out := make([]anthropicTool, len(tools))
	for i, t := range tools {
		out[i] = anthropicTool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			InputSchema: t.Function.Parameters,
		}
	}
	return out
}

// ─── SSE stream parser ──────────────────────────────────────────────────────

type sseData struct {
	Type  string `json:"type"`
	Index int    `json:"index"`

	// content_block_start
	ContentBlock *struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block,omitempty"`

	// content_block_delta
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text,omitempty"`
		PartialJSON string `json:"partial_json,omitempty"`
	} `json:"delta,omitempty"`
}

type blockAccum struct {
	blockType   string
	toolID      string
	toolName    string
	argsBuilder strings.Builder
}

func parseStream(body io.Reader, onEvent openrouter.StreamHandler) (openrouter.Message, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	blocks := make(map[int]*blockAccum)
	var contentBuilder strings.Builder
	var toolCalls []openrouter.ToolCall

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}

		var evt sseData
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			continue
		}

		switch evt.Type {
		case "content_block_start":
			if evt.ContentBlock == nil {
				continue
			}
			b := &blockAccum{blockType: evt.ContentBlock.Type}
			if evt.ContentBlock.Type == "tool_use" {
				b.toolID = evt.ContentBlock.ID
				b.toolName = evt.ContentBlock.Name
			}
			blocks[evt.Index] = b

		case "content_block_delta":
			b := blocks[evt.Index]
			if b == nil || evt.Delta == nil {
				continue
			}
			switch evt.Delta.Type {
			case "text_delta":
				contentBuilder.WriteString(evt.Delta.Text)
				onEvent(openrouter.StreamEvent{Delta: openrouter.StreamDelta{Content: evt.Delta.Text}})
			case "input_json_delta":
				b.argsBuilder.WriteString(evt.Delta.PartialJSON)
			}

		case "content_block_stop":
			b := blocks[evt.Index]
			if b != nil && b.blockType == "tool_use" {
				toolCalls = append(toolCalls, openrouter.ToolCall{
					ID:   b.toolID,
					Type: "function",
					Function: openrouter.ToolFunctionCall{
						Name:      b.toolName,
						Arguments: b.argsBuilder.String(),
					},
				})
			}

		case "message_stop":
			onEvent(openrouter.StreamEvent{Done: true})
		}
	}

	if err := scanner.Err(); err != nil {
		return openrouter.Message{}, fmt.Errorf("read stream: %w", err)
	}

	// Sort tool calls by order of appearance (map iteration is unordered).
	sort.Slice(toolCalls, func(i, j int) bool {
		return toolCalls[i].ID < toolCalls[j].ID
	})

	return openrouter.Message{
		Role:      "assistant",
		Content:   contentBuilder.String(),
		ToolCalls: toolCalls,
	}, nil
}

// ─── HTTP error helper ──────────────────────────────────────────────────────

func parseHTTPError(resp *http.Response) error {
	var out struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	b, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(b, &out); err == nil && out.Error.Message != "" {
		return fmt.Errorf("anthropic error (%d): %s", resp.StatusCode, out.Error.Message)
	}
	return fmt.Errorf("anthropic error (%d)", resp.StatusCode)
}
