package openrouter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const chatCompletionsURL = "https://openrouter.ai/api/v1/chat/completions"

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type StreamDelta struct {
	Content string
}

type StreamEvent struct {
	Delta StreamDelta
	Done  bool
}

type StreamHandler func(StreamEvent)

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

func (c *Client) StreamChatCompletion(ctx context.Context, model string, messages []Message, onEvent StreamHandler) error {
	if strings.TrimSpace(model) == "" {
		return fmt.Errorf("model is required")
	}
	if onEvent == nil {
		return fmt.Errorf("stream handler is required")
	}

	payload := map[string]any{
		"model":    model,
		"messages": messages,
		"stream":   true,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatCompletionsURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
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
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseHTTPError(resp)
	}

	scanner := bufio.NewScanner(resp.Body)
	// allow larger SSE chunks than scanner default (64K)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

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
			return nil
		}

		var chunk sseChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return fmt.Errorf("decode stream chunk: %w", err)
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				onEvent(StreamEvent{Delta: StreamDelta{Content: choice.Delta.Content}})
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read stream: %w", err)
	}

	return nil
}

type sseChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
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
