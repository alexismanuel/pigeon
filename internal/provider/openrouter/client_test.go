package openrouter

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestStreamChatCompletion_Success(t *testing.T) {
	sse := strings.Join([]string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}",
		"",
		"data: {\"choices\":[{\"delta\":{\"content\":\" world\"}}]}",
		"",
		"data: [DONE]",
		"",
	}, "\n")

	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", req.Method)
		}
		if req.URL.String() != chatCompletionsURL {
			t.Fatalf("unexpected url: %s", req.URL.String())
		}
		if got := req.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("unexpected auth header: %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(sse)),
			Header:     make(http.Header),
		}, nil
	})}

	client := NewClient("test-key", httpClient)
	var tokens []string
	gotDone := false

	msg, err := client.StreamChatCompletion(context.Background(), "openai/gpt-4o-mini", []Message{{Role: "user", Content: "hi"}}, nil, func(ev StreamEvent) {
		if ev.Done {
			gotDone = true
			return
		}
		if ev.Delta.Content != "" {
			tokens = append(tokens, ev.Delta.Content)
		}
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Join(tokens, "") != "Hello world" {
		t.Fatalf("unexpected tokens: %v", tokens)
	}
	if !gotDone {
		t.Fatalf("expected done event")
	}
	if msg.Role != "assistant" || msg.Content != "Hello world" {
		t.Fatalf("unexpected final message: %+v", msg)
	}
}

func TestStreamChatCompletion_ToolCallAggregation(t *testing.T) {
	sse := strings.Join([]string{
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"read\",\"arguments\":\"{\\\"path\\\":\\\"REA\"}}]}}]}",
		"",
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"DME.md\\\"}\"}}]}}]}",
		"",
		"data: [DONE]",
		"",
	}, "\n")

	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(sse)),
			Header:     make(http.Header),
		}, nil
	})}

	client := NewClient("test-key", httpClient)
	msg, err := client.StreamChatCompletion(context.Background(), "openai/gpt-4o-mini", []Message{{Role: "user", Content: "read file"}}, nil, func(StreamEvent) {})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("expected one tool call, got %d", len(msg.ToolCalls))
	}
	call := msg.ToolCalls[0]
	if call.ID != "call_1" || call.Function.Name != "read" {
		t.Fatalf("unexpected tool call metadata: %+v", call)
	}
	if call.Function.Arguments != `{"path":"README.md"}` {
		t.Fatalf("unexpected tool call args: %q", call.Function.Arguments)
	}
}

func TestListModels_Success(t *testing.T) {
	payload := `{"data":[{"id":"openai/gpt-4o-mini","name":"GPT-4o Mini","context_length":128000},{"id":"anthropic/claude-3.5-sonnet","name":"Claude 3.5 Sonnet","context_length":200000}]}`
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			t.Fatalf("unexpected method: %s", req.Method)
		}
		if req.URL.String() != modelsURL {
			t.Fatalf("unexpected url: %s", req.URL.String())
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(payload)), Header: make(http.Header)}, nil
	})}
	client := NewClient("test-key", httpClient)
	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].ID == models[1].ID {
		t.Fatalf("expected distinct models")
	}
}

func TestStreamChatCompletion_HTTPError(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"bad key"}}`)),
			Header:     make(http.Header),
		}, nil
	})}

	client := NewClient("test-key", httpClient)
	_, err := client.StreamChatCompletion(context.Background(), "openai/gpt-4o-mini", []Message{{Role: "user", Content: "hi"}}, nil, func(StreamEvent) {})
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "bad key") {
		t.Fatalf("expected bad key error, got: %v", err)
	}
}

// ── NewClient / SetAttribution ─────────────────────────────────────────────────

func TestNewClient_DefaultHTTPClient(t *testing.T) {
	c := NewClient("key", nil)
	if c.httpClient != http.DefaultClient {
		t.Error("expected default http client")
	}
	if c.appName != "pigeon" {
		t.Errorf("expected default appName 'pigeon', got %q", c.appName)
	}
}

func TestSetAttribution(t *testing.T) {
	c := NewClient("key", nil)
	c.SetAttribution("myapp", "https://example.com")
	if c.appName != "myapp" {
		t.Errorf("expected 'myapp', got %q", c.appName)
	}
	if c.appURL != "https://example.com" {
		t.Errorf("expected URL, got %q", c.appURL)
	}
}

func TestSetAttribution_TrimsSpace(t *testing.T) {
	c := NewClient("key", nil)
	c.SetAttribution("  trimmed  ", "  url  ")
	if c.appName != "trimmed" {
		t.Errorf("expected trimmed name, got %q", c.appName)
	}
}

// ── ListModels error paths ─────────────────────────────────────────────────────

func TestListModels_HTTPError(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"invalid key"}}`)),
			Header:     make(http.Header),
		}, nil
	})}
	c := NewClient("bad-key", httpClient)
	_, err := c.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if !strings.Contains(err.Error(), "invalid key") {
		t.Errorf("expected error message, got %q", err.Error())
	}
}

func TestListModels_HTTPErrorNoBody(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     make(http.Header),
		}, nil
	})}
	c := NewClient("key", httpClient)
	_, err := c.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected error for 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected status in error, got %q", err.Error())
	}
}

func TestListModels_SkipsEmptyIDItems(t *testing.T) {
	body := `{"data":[{"id":"","name":"empty"},{"id":"openai/gpt-4","name":"GPT-4","context_length":8192}]}`
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})}
	c := NewClient("key", httpClient)
	models, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 1 || models[0].ID != "openai/gpt-4" {
		t.Errorf("expected 1 model with id openai/gpt-4, got %+v", models)
	}
}

func TestListModels_UsesIDIfNameEmpty(t *testing.T) {
	body := `{"data":[{"id":"openai/gpt-4","name":"","context_length":8192}]}`
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})}
	c := NewClient("key", httpClient)
	models, _ := c.ListModels(context.Background())
	if len(models) != 1 || models[0].Name != "openai/gpt-4" {
		t.Errorf("expected Name=ID fallback, got %+v", models)
	}
}

// ── parseHTTPError ─────────────────────────────────────────────────────────────

func TestParseHTTPError_WithMessage(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"forbidden"}}`)),
	}
	err := parseHTTPError(resp)
	if err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Errorf("expected error with 'forbidden', got %v", err)
	}
}

func TestParseHTTPError_InvalidJSON(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Body:       io.NopCloser(strings.NewReader("not json")),
	}
	err := parseHTTPError(resp)
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Errorf("expected status code in error, got %v", err)
	}
}


