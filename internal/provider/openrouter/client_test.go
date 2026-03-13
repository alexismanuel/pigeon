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

	err := client.StreamChatCompletion(context.Background(), "openai/gpt-4o-mini", []Message{{Role: "user", Content: "hi"}}, func(ev StreamEvent) {
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
	err := client.StreamChatCompletion(context.Background(), "openai/gpt-4o-mini", []Message{{Role: "user", Content: "hi"}}, func(StreamEvent) {})
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "bad key") {
		t.Fatalf("expected bad key error, got: %v", err)
	}
}
