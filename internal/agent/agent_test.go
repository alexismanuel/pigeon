package agent

import (
	"context"
	"fmt"
	"testing"

	"pigeon/internal/provider/openrouter"
)

type fakeStreamingClient struct {
	err    error
	events []openrouter.StreamEvent
}

func (f fakeStreamingClient) StreamChatCompletion(_ context.Context, model string, messages []openrouter.Message, onEvent openrouter.StreamHandler) error {
	if model == "" {
		return fmt.Errorf("missing model")
	}
	if len(messages) != 1 || messages[0].Role != "user" {
		return fmt.Errorf("unexpected messages")
	}
	if f.err != nil {
		return f.err
	}
	for _, ev := range f.events {
		onEvent(ev)
	}
	return nil
}

func TestAgentRunTurn_Success(t *testing.T) {
	a := New(fakeStreamingClient{events: []openrouter.StreamEvent{
		{Delta: openrouter.StreamDelta{Content: "Hello"}},
		{Delta: openrouter.StreamDelta{Content: " there"}},
		{Done: true},
	}})

	var streamed string
	final, err := a.RunTurn(context.Background(), "openai/gpt-4o-mini", "hi", func(tok string) {
		streamed += tok
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if final != "Hello there" {
		t.Fatalf("unexpected final: %q", final)
	}
	if streamed != "Hello there" {
		t.Fatalf("unexpected streamed tokens: %q", streamed)
	}
}

func TestAgentRunTurn_Validation(t *testing.T) {
	a := New(nil)
	if _, err := a.RunTurn(context.Background(), "model", "hello", nil); err == nil {
		t.Fatalf("expected nil client error")
	}

	a = New(fakeStreamingClient{})
	if _, err := a.RunTurn(context.Background(), "model", "   ", nil); err == nil {
		t.Fatalf("expected empty input error")
	}
}
