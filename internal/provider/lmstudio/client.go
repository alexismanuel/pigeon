// Package lmstudio provides a streaming OpenAI-compatible client for LM Studio
// (and any other local OpenAI-compat server).
//
// Configure the server URL via the LMSTUDIO_BASE_URL environment variable.
// Defaults to http://localhost:1234/v1 when unset.
// No API key is required; pass an empty string or a placeholder.
package lmstudio

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"

	"pigeon/internal/provider/openrouter"
)

const (
	// DefaultBaseURL is the standard LM Studio local server address.
	DefaultBaseURL = "http://localhost:1234/v1"

	chatPath   = "/chat/completions"
	modelsPath = "/models"
)

// BaseURL returns the configured LM Studio base URL, falling back to
// DefaultBaseURL if LMSTUDIO_BASE_URL is unset or empty.
func BaseURL() string {
	if v := strings.TrimSpace(os.Getenv("LMSTUDIO_BASE_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return DefaultBaseURL
}

// Client is an LM Studio / OpenAI-compatible streaming client.
type Client struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string // optional; sent as Bearer if non-empty
}

// NewClient creates an LM Studio client.  baseURL defaults to BaseURL() when
// empty.  httpClient defaults to http.DefaultClient when nil.
func NewClient(baseURL, apiKey string, httpClient *http.Client) *Client {
	if baseURL == "" {
		baseURL = BaseURL()
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{httpClient: httpClient, baseURL: baseURL, apiKey: apiKey}
}

// ListModels queries the /models endpoint and returns the loaded models.
// Returns an empty slice (not an error) when LM Studio is not running.
func (c *Client) ListModels(ctx context.Context) ([]openrouter.ModelInfo, error) {
	u, err := url.JoinPath(c.baseURL, modelsPath)
	if err != nil {
		return nil, fmt.Errorf("build models URL: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	c.setAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// LM Studio is not running — return an empty list rather than an error
		// so the multi-provider can still show models from other providers.
		return nil, nil //nolint:nilerr
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseHTTPError(resp)
	}

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode models: %w", err)
	}

	models := make([]openrouter.ModelInfo, 0, len(payload.Data))
	for _, m := range payload.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		models = append(models, openrouter.ModelInfo{
			ID:       id,
			Name:     id, // LM Studio uses the model path as both ID and name
			Provider: "lmstudio",
		})
	}
	sort.Slice(models, func(i, j int) bool {
		return models[i].ID < models[j].ID
	})
	return models, nil
}

// StreamChatCompletion sends an OpenAI-compat streaming chat completion request
// to the LM Studio server.
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
		return openrouter.Message{}, fmt.Errorf("marshal request: %w", err)
	}

	u, err := url.JoinPath(c.baseURL, chatPath)
	if err != nil {
		return openrouter.Message{}, fmt.Errorf("build URL: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return openrouter.Message{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return openrouter.Message{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return openrouter.Message{}, parseHTTPError(resp)
	}

	return parseStream(resp.Body, onEvent)
}

func (c *Client) setAuth(req *http.Request) {
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
}

// ─── SSE stream parser (OpenAI-compat, same as openrouter) ─────────────────

func parseStream(body io.Reader, onEvent openrouter.StreamHandler) (openrouter.Message, error) {
	scanner := bufio.NewScanner(body)
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
			onEvent(openrouter.StreamEvent{Done: true})
			return openrouter.Message{
				Role:      "assistant",
				Content:   contentBuilder.String(),
				ToolCalls: finalizeToolCalls(toolCallsByIndex),
			}, nil
		}

		var chunk sseChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				contentBuilder.WriteString(choice.Delta.Content)
				onEvent(openrouter.StreamEvent{Delta: openrouter.StreamDelta{Content: choice.Delta.Content}})
			}
			for _, tc := range choice.Delta.ToolCalls {
				b := toolCallsByIndex[tc.Index]
				if b == nil {
					b = &toolCallBuilder{}
					toolCallsByIndex[tc.Index] = b
				}
				if tc.ID != "" {
					b.ID = tc.ID
				}
				if tc.Type != "" {
					b.Type = tc.Type
				}
				if tc.Function.Name != "" {
					b.Name = tc.Function.Name
				}
				if tc.Function.Arguments != "" {
					b.Arguments.WriteString(tc.Function.Arguments)
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
	}, nil
}

type toolCallBuilder struct {
	ID        string
	Type      string
	Name      string
	Arguments strings.Builder
}

func finalizeToolCalls(m map[int]*toolCallBuilder) []openrouter.ToolCall {
	if len(m) == 0 {
		return nil
	}
	indices := make([]int, 0, len(m))
	for idx := range m {
		indices = append(indices, idx)
	}
	sort.Ints(indices)
	out := make([]openrouter.ToolCall, 0, len(indices))
	for _, idx := range indices {
		b := m[idx]
		if b == nil {
			continue
		}
		out = append(out, openrouter.ToolCall{
			ID:   b.ID,
			Type: b.Type,
			Function: openrouter.ToolFunctionCall{
				Name:      b.Name,
				Arguments: b.Arguments.String(),
			},
		})
	}
	return out
}

type sseChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
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

func parseHTTPError(resp *http.Response) error {
	var out struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	b, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(b, &out); err == nil && out.Error.Message != "" {
		return fmt.Errorf("lmstudio error (%d): %s", resp.StatusCode, out.Error.Message)
	}
	return fmt.Errorf("lmstudio error (%d)", resp.StatusCode)
}
