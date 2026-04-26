// Package zai provides a streaming chat-completion client for the Zai GLM
// API.  The Zai API is OpenAI-compatible and is hosted at api.z.ai.
//
// Users obtain API keys from their Zai account (https://z.ai) under
// API Key settings.  The key is sent as a Bearer token in the Authorization
// header, exactly like OpenRouter.
//
// A Zai "coding plan" subscription provides a monthly allocation of credits
// that are consumed per request — similar to how Claude Code uses an Anthropic
// Pro/Max subscription.
package zai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"pigeon/internal/provider/openrouter"
)

const (
	// baseURL is the Zai coding PaaS endpoint (matches pi's models.generated.js).
	baseURL = "https://api.z.ai/api/coding/paas/v4"

	chatCompletionsPath = "/chat/completions"
)



// Client is a Zai API client implementing pigeon's StreamingClient and
// modelCatalog interfaces.  It is safe for concurrent use.
type Client struct {
	httpClient *http.Client
	apiKey     string
	appName    string
	appURL     string
}

// NewClient creates a new Zai client.  Pass nil for httpClient to use the
// default http.Client.
func NewClient(apiKey string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		httpClient: httpClient,
		apiKey:     apiKey,
		appName:    "pigeon",
	}
}

// SetAttribution sets the app name and URL sent in X-Title / HTTP-Referer
// headers.
func (c *Client) SetAttribution(appName, appURL string) {
	c.appName = strings.TrimSpace(appName)
	c.appURL = strings.TrimSpace(appURL)
}

// models is the curated list of Zai GLM models, matching pi's
// models.generated.js. Context lengths are included here directly since Zai
// does not expose a /v1/models discovery endpoint.
var models = []openrouter.ModelInfo{
	{ID: "glm-4.5", Name: "GLM-4.5", ContextLength: 131072, Provider: "zai"},
	{ID: "glm-4.5-air", Name: "GLM-4.5-Air", ContextLength: 131072, Provider: "zai"},
	{ID: "glm-4.5-flash", Name: "GLM-4.5-Flash", ContextLength: 131072, Provider: "zai"},
	{ID: "glm-4.5v", Name: "GLM-4.5V", ContextLength: 64000, Provider: "zai"},
	{ID: "glm-4.6", Name: "GLM-4.6", ContextLength: 204800, Provider: "zai"},
	{ID: "glm-4.6v", Name: "GLM-4.6V", ContextLength: 128000, Provider: "zai"},
	{ID: "glm-4.7", Name: "GLM-4.7", ContextLength: 204800, Provider: "zai"},
	{ID: "glm-4.7-flash", Name: "GLM-4.7-Flash", ContextLength: 200000, Provider: "zai"},
	{ID: "glm-4.7-flashx", Name: "GLM-4.7-FlashX", ContextLength: 200000, Provider: "zai"},
	{ID: "glm-5", Name: "GLM-5", ContextLength: 204800, Provider: "zai"},
	{ID: "glm-5-turbo", Name: "GLM-5-Turbo", ContextLength: 200000, Provider: "zai"},
	{ID: "glm-5.1", Name: "GLM-5.1", ContextLength: 200000, Provider: "zai"},
	{ID: "glm-5v-turbo", Name: "GLM-5V-Turbo", ContextLength: 200000, Provider: "zai"},
}

// ListModels returns the curated Zai GLM model list.
func (c *Client) ListModels(_ context.Context) ([]openrouter.ModelInfo, error) {
	out := make([]openrouter.ModelInfo, len(models))
	copy(out, models)
	return out, nil
}


// StreamChatCompletion streams a chat completion from the Zai API.  The Zai
// API is OpenAI-compatible so the request/response format is identical to
// OpenRouter.
func (c *Client) StreamChatCompletion(
	ctx context.Context,
	model string,
	messages []openrouter.Message,
	tools []openrouter.ToolDefinition,
	onEvent openrouter.StreamHandler,
) (openrouter.Message, error) {
	if strings.TrimSpace(model) == "" {
		return openrouter.Message{}, fmt.Errorf("model is required")
	}
	if onEvent == nil {
		return openrouter.Message{}, fmt.Errorf("stream handler is required")
	}

	payload := map[string]any{
		"model":          model,
		"messages":       messages,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}
	if len(tools) > 0 {
		payload["tools"] = tools
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return openrouter.Message{}, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+chatCompletionsPath, bytes.NewReader(body))
	if err != nil {
		return openrouter.Message{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "pigeon")
	if c.appName != "" {
		req.Header.Set("X-Title", c.appName)
	}
	if c.appURL != "" {
		req.Header.Set("HTTP-Referer", c.appURL)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return openrouter.Message{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return openrouter.Message{}, parseHTTPError(resp)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var contentBuilder strings.Builder
	toolCallsByIndex := map[int]*toolCallBuilder{}
	var usage openrouter.Usage

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			onEvent(openrouter.StreamEvent{Done: true})
			return openrouter.Message{
				Role:      "assistant",
				Content:   contentBuilder.String(),
				ToolCalls: finalizeToolCalls(toolCallsByIndex),
				Usage:     usage,
			}, nil
		}

		var chunk sseChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return openrouter.Message{}, fmt.Errorf("decode stream chunk: %w", err)
		}
		if chunk.Usage != nil {
			usage.InputTokens = chunk.Usage.PromptTokens
			usage.OutputTokens = chunk.Usage.CompletionTokens
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Reasoning != "" {
				onEvent(openrouter.StreamEvent{Delta: openrouter.StreamDelta{Reasoning: choice.Delta.Reasoning}})
			}
			if choice.Delta.Content != "" {
				contentBuilder.WriteString(choice.Delta.Content)
				onEvent(openrouter.StreamEvent{Delta: openrouter.StreamDelta{Content: choice.Delta.Content}})
			}
			for _, toolCallDelta := range choice.Delta.ToolCalls {
				builder := toolCallsByIndex[toolCallDelta.Index]
				if builder == nil {
					builder = &toolCallBuilder{}
					toolCallsByIndex[toolCallDelta.Index] = builder
				}
				if toolCallDelta.ID != "" {
					builder.ID = toolCallDelta.ID
				}
				if toolCallDelta.Type != "" {
					builder.Type = toolCallDelta.Type
				}
				if toolCallDelta.Function.Name != "" {
					builder.Name = toolCallDelta.Function.Name
				}
				if toolCallDelta.Function.Arguments != "" {
					builder.Arguments.WriteString(toolCallDelta.Function.Arguments)
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return openrouter.Message{}, fmt.Errorf("read stream: %w", err)
	}

	return openrouter.Message{
		Role:      "assistant",
		Content:   contentBuilder.String(),
		ToolCalls: finalizeToolCalls(toolCallsByIndex),
		Usage:     usage,
	}, nil
}

// ─── internal types ──────────────────────────────────────────────────────────

type toolCallBuilder struct {
	ID        string
	Type      string
	Name      string
	Arguments strings.Builder
}

func finalizeToolCalls(toolCallsByIndex map[int]*toolCallBuilder) []openrouter.ToolCall {
	if len(toolCallsByIndex) == 0 {
		return nil
	}
	indices := make([]int, 0, len(toolCallsByIndex))
	for idx := range toolCallsByIndex {
		indices = append(indices, idx)
	}
	sort.Ints(indices)

	out := make([]openrouter.ToolCall, 0, len(indices))
	for _, idx := range indices {
		item := toolCallsByIndex[idx]
		if item == nil {
			continue
		}
		out = append(out, openrouter.ToolCall{
			ID:   item.ID,
			Type: item.Type,
			Function: openrouter.ToolFunctionCall{
				Name:      item.Name,
				Arguments: item.Arguments.String(),
			},
		})
	}
	return out
}

type sseChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			Reasoning string `json:"reasoning"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

type errorResponse struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func parseHTTPError(resp *http.Response) error {
	var out errorResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err == nil && strings.TrimSpace(out.Error.Message) != "" {
		return fmt.Errorf("zai error (%d): %s", resp.StatusCode, out.Error.Message)
	}
	return fmt.Errorf("zai error (%d)", resp.StatusCode)
}
