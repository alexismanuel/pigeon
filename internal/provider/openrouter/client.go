package openrouter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

const (
	chatCompletionsURL = "https://openrouter.ai/api/v1/chat/completions"
	modelsURL          = "https://openrouter.ai/api/v1/models"
)

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

type ToolDefinition struct {
	Type     string                 `json:"type"`
	Function ToolFunctionDefinition `json:"function"`
}

type ToolFunctionDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type ToolCall struct {
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function ToolFunctionCall `json:"function"`
}

type ToolFunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type StreamDelta struct {
	Content   string
	Reasoning string // thinking tokens from reasoning models (e.g. Kimi K2)
}

type StreamEvent struct {
	Delta StreamDelta
	Done  bool
}

type StreamHandler func(StreamEvent)

type ModelInfo struct {
	ID            string
	Name          string
	ContextLength int
}

type Client struct {
	httpClient *http.Client
	apiKey     string
	appName    string
	appURL     string
}

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

func (c *Client) SetAttribution(appName, appURL string) {
	c.appName = strings.TrimSpace(appName)
	c.appURL = strings.TrimSpace(appURL)
}

func (c *Client) ListModels(ctx context.Context) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseHTTPError(resp)
	}

	var payload struct {
		Data []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Context int    `json:"context_length"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode models response: %w", err)
	}

	models := make([]ModelInfo, 0, len(payload.Data))
	for _, item := range payload.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = id
		}
		models = append(models, ModelInfo{
			ID:            id,
			Name:          name,
			ContextLength: item.Context,
		})
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].Name == models[j].Name {
			return models[i].ID < models[j].ID
		}
		return models[i].Name < models[j].Name
	})
	return models, nil
}

func (c *Client) StreamChatCompletion(
	ctx context.Context,
	model string,
	messages []Message,
	tools []ToolDefinition,
	onEvent StreamHandler,
) (Message, error) {
	if strings.TrimSpace(model) == "" {
		return Message{}, fmt.Errorf("model is required")
	}
	if onEvent == nil {
		return Message{}, fmt.Errorf("stream handler is required")
	}

	payload := map[string]any{
		"model":    model,
		"messages": messages,
		"stream":   true,
	}
	if len(tools) > 0 {
		payload["tools"] = tools
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return Message{}, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatCompletionsURL, bytes.NewReader(body))
	if err != nil {
		return Message{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	if c.appName != "" {
		req.Header.Set("X-Title", c.appName)
	}
	if c.appURL != "" {
		req.Header.Set("HTTP-Referer", c.appURL)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Message{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Message{}, parseHTTPError(resp)
	}

	scanner := bufio.NewScanner(resp.Body)
	// allow larger SSE chunks than scanner default (64K)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var contentBuilder strings.Builder
	toolCallsByIndex := map[int]*toolCallBuilder{}

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
			onEvent(StreamEvent{Done: true})
			return Message{
				Role:      "assistant",
				Content:   contentBuilder.String(),
				ToolCalls: finalizeToolCalls(toolCallsByIndex),
			}, nil
		}

		var chunk sseChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return Message{}, fmt.Errorf("decode stream chunk: %w", err)
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Reasoning != "" {
				onEvent(StreamEvent{Delta: StreamDelta{Reasoning: choice.Delta.Reasoning}})
			}
			if choice.Delta.Content != "" {
				contentBuilder.WriteString(choice.Delta.Content)
				onEvent(StreamEvent{Delta: StreamDelta{Content: choice.Delta.Content}})
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
		return Message{}, fmt.Errorf("read stream: %w", err)
	}

	return Message{
		Role:      "assistant",
		Content:   contentBuilder.String(),
		ToolCalls: finalizeToolCalls(toolCallsByIndex),
	}, nil
}

type toolCallBuilder struct {
	ID        string
	Type      string
	Name      string
	Arguments strings.Builder
}

func finalizeToolCalls(toolCallsByIndex map[int]*toolCallBuilder) []ToolCall {
	if len(toolCallsByIndex) == 0 {
		return nil
	}
	indices := make([]int, 0, len(toolCallsByIndex))
	for idx := range toolCallsByIndex {
		indices = append(indices, idx)
	}
	sort.Ints(indices)

	out := make([]ToolCall, 0, len(indices))
	for _, idx := range indices {
		item := toolCallsByIndex[idx]
		if item == nil {
			continue
		}
		out = append(out, ToolCall{
			ID:   item.ID,
			Type: item.Type,
			Function: ToolFunctionCall{
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
			Reasoning string `json:"reasoning"` // thinking tokens from reasoning models
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
}

type errorResponse struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func parseHTTPError(resp *http.Response) error {
	var out errorResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err == nil && strings.TrimSpace(out.Error.Message) != "" {
		return fmt.Errorf("openrouter error (%d): %s", resp.StatusCode, out.Error.Message)
	}
	return fmt.Errorf("openrouter error (%d)", resp.StatusCode)
}
