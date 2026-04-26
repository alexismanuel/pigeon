package zai

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"pigeon/internal/provider/openrouter"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestListModels_returnsCurated(t *testing.T) {
	c := NewClient("test-key", nil)
	got, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(got) != len(models) {
		t.Fatalf("expected %d models, got %d", len(models), len(got))
	}
	for _, m := range got {
		if m.Provider != "zai" {
			t.Errorf("model %q has provider %q, want %q", m.ID, m.Provider, "zai")
		}
	}
	// Verify GLM-5.1 is present.
	found := false
	for _, m := range got {
		if m.ID == "glm-5.1" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected glm-5.1 in model list")
	}
}





func TestStreamChatCompletion_success(t *testing.T) {
	sse := strings.Join([]string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}",
		"",
		"data: {\"choices\":[{\"delta\":{\"content\":\" world\"}}]}",
		"",
		"data: [DONE]",
		"",
	}, "\n")

	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("wrong Authorization: %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(sse)),
			Header:     make(http.Header),
		}, nil
	})}

	c := NewClient("test-key", httpClient)
	var tokens []string
	gotDone := false

	msg, err := c.StreamChatCompletion(context.Background(), "gpt-4o", []openrouter.Message{{Role: "user", Content: "hi"}}, nil, func(ev openrouter.StreamEvent) {
		if ev.Done {
			gotDone = true
			return
		}
		if ev.Delta.Content != "" {
			tokens = append(tokens, ev.Delta.Content)
		}
	})
	if err != nil {
		t.Fatalf("StreamChatCompletion: %v", err)
	}
	if strings.Join(tokens, "") != "Hello world" {
		t.Errorf("tokens = %v, want [Hello world]", tokens)
	}
	if !gotDone {
		t.Error("expected done event")
	}
	if msg.Content != "Hello world" {
		t.Errorf("content = %q, want %q", msg.Content, "Hello world")
	}
}

func TestStreamChatCompletion_httpError(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"Invalid API Key"}}`)),
			Header:     make(http.Header),
		}, nil
	})}

	c := NewClient("bad-key", httpClient)
	_, err := c.StreamChatCompletion(context.Background(), "gpt-4o", nil, nil, func(e openrouter.StreamEvent) {})
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
	if !strings.Contains(err.Error(), "Invalid API Key") {
		t.Errorf("error = %q, want 'Invalid API Key'", err.Error())
	}
}

func TestStreamChatCompletion_modelRequired(t *testing.T) {
	c := NewClient("key", nil)
	_, err := c.StreamChatCompletion(context.Background(), "", nil, nil, func(e openrouter.StreamEvent) {})
	if err == nil {
		t.Fatal("expected error for empty model")
	}
}

func TestStreamChatCompletion_handlerRequired(t *testing.T) {
	c := NewClient("key", nil)
	_, err := c.StreamChatCompletion(context.Background(), "gpt-4o", nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for nil handler")
	}
}

func TestStreamChatCompletion_toolCalls(t *testing.T) {
	sse := strings.Join([]string{
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"read\",\"arguments\":\"{\\\"path\\\":\\\"main\"}}]}}]}",
		"",
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\".go\\\"}\"}}]}}]}",
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

	c := NewClient("test-key", httpClient)
	msg, err := c.StreamChatCompletion(context.Background(), "gpt-4o", []openrouter.Message{{Role: "user", Content: "read"}}, nil, func(openrouter.StreamEvent) {})
	if err != nil {
		t.Fatalf("StreamChatCompletion: %v", err)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(msg.ToolCalls))
	}
	if msg.ToolCalls[0].ID != "call_1" || msg.ToolCalls[0].Function.Name != "read" {
		t.Errorf("unexpected tool call: %+v", msg.ToolCalls[0])
	}
	if msg.ToolCalls[0].Function.Arguments != `{"path":"main.go"}` {
		t.Errorf("arguments = %q", msg.ToolCalls[0].Function.Arguments)
	}
}

func TestStreamChatCompletion_usageAndReasoning(t *testing.T) {
	sse := strings.Join([]string{
		"data: {\"choices\":[{\"delta\":{\"reasoning\":\"let me think\"}}]}",
		"",
		"data: {\"choices\":[{\"delta\":{\"content\":\"answer\"}}]}",
		"",
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5}}",
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

	c := NewClient("test-key", httpClient)
	var reasoning []string
	msg, err := c.StreamChatCompletion(context.Background(), "deepseek-reasoner", nil, nil, func(ev openrouter.StreamEvent) {
		if ev.Delta.Reasoning != "" {
			reasoning = append(reasoning, ev.Delta.Reasoning)
		}
	})
	if err != nil {
		t.Fatalf("StreamChatCompletion: %v", err)
	}
	if msg.Content != "answer" {
		t.Errorf("content = %q, want 'answer'", msg.Content)
	}
	if len(reasoning) != 1 || reasoning[0] != "let me think" {
		t.Errorf("reasoning = %v", reasoning)
	}
	if msg.Usage.InputTokens != 10 || msg.Usage.OutputTokens != 5 {
		t.Errorf("usage = %+v", msg.Usage)
	}
}

func TestNewClient_defaultHTTPClient(t *testing.T) {
	c := NewClient("key", nil)
	if c.httpClient != http.DefaultClient {
		t.Error("expected default http client")
	}
}

func TestSetAttribution(t *testing.T) {
	c := NewClient("key", nil)
	c.SetAttribution("myapp", "https://example.com")
	if c.appName != "myapp" {
		t.Errorf("appName = %q, want 'myapp'", c.appName)
	}
	if c.appURL != "https://example.com" {
		t.Errorf("appURL = %q", c.appURL)
	}
}

func TestParseHTTPError_noMessage(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Body:       io.NopCloser(strings.NewReader("not json")),
	}
	err := parseHTTPError(resp)
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Errorf("expected 502 in error, got %v", err)
	}
}
