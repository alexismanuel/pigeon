package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"pigeon/internal/provider/openrouter"
)

type fakeStreamingClient struct {
	err      error
	messages []openrouter.Message
	calls    int
}

func (f *fakeStreamingClient) StreamChatCompletion(
	_ context.Context,
	model string,
	messages []openrouter.Message,
	_ []openrouter.ToolDefinition,
	onEvent openrouter.StreamHandler,
) (openrouter.Message, error) {
	if model == "" {
		return openrouter.Message{}, fmt.Errorf("missing model")
	}
	if f.err != nil {
		return openrouter.Message{}, f.err
	}
	if f.calls >= len(f.messages) {
		return openrouter.Message{}, fmt.Errorf("unexpected extra call")
	}
	msg := f.messages[f.calls]
	f.calls++
	if strings.TrimSpace(msg.Content) != "" {
		onEvent(openrouter.StreamEvent{Delta: openrouter.StreamDelta{Content: msg.Content}})
	}
	onEvent(openrouter.StreamEvent{Done: true})
	return msg, nil
}

type fakeToolExecutor struct {
	defs    []openrouter.ToolDefinition
	results map[string]string
	errs    map[string]error
	calls   []string
}

func (f *fakeToolExecutor) Definitions() []openrouter.ToolDefinition { return f.defs }

func (f *fakeToolExecutor) Execute(_ context.Context, name, argumentsJSON string) (string, string, error) {
	f.calls = append(f.calls, name+":"+argumentsJSON)
	return f.results[name], "", f.errs[name]
}

func TestAgentRunTurn_SuccessNoTools(t *testing.T) {
	client := &fakeStreamingClient{messages: []openrouter.Message{{Role: "assistant", Content: "Hello there"}}}
	a := NewWithTools(client, &fakeToolExecutor{})

	var streamed string
	newMessages, err := a.RunTurn(context.Background(), "openai/gpt-4o-mini", nil, "hi", TurnCallbacks{
		OnToken: func(tok string) {
			streamed += tok
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(newMessages) != 2 {
		t.Fatalf("expected 2 messages (user+assistant), got %d", len(newMessages))
	}
	if newMessages[1].Content != "Hello there" {
		t.Fatalf("unexpected final assistant: %q", newMessages[1].Content)
	}
	if streamed != "Hello there" {
		t.Fatalf("unexpected streamed tokens: %q", streamed)
	}
}

func TestAgentRunTurn_WithHistoryAndToolCall(t *testing.T) {
	client := &fakeStreamingClient{messages: []openrouter.Message{
		{
			Role: "assistant",
			ToolCalls: []openrouter.ToolCall{{
				ID:   "call_1",
				Type: "function",
				Function: openrouter.ToolFunctionCall{
					Name:      "read",
					Arguments: `{"path":"README.md"}`,
				},
			}},
		},
		{Role: "assistant", Content: "Done after tool"},
	}}
	toolExec := &fakeToolExecutor{results: map[string]string{"read": "# README"}}
	a := NewWithTools(client, toolExec)

	var events []ToolEvent
	newMessages, err := a.RunTurn(
		context.Background(),
		"openai/gpt-4o-mini",
		[]openrouter.Message{{Role: "user", Content: "old question"}},
		"summarize readme",
		TurnCallbacks{OnToolEvent: func(evt ToolEvent) { events = append(events, evt) }},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(newMessages) != 4 {
		t.Fatalf("expected 4 new messages (user,assistant,tool,assistant), got %d", len(newMessages))
	}
	if newMessages[3].Content != "Done after tool" {
		t.Fatalf("unexpected final: %q", newMessages[3].Content)
	}
	if len(toolExec.calls) != 1 {
		t.Fatalf("expected one tool call, got %d", len(toolExec.calls))
	}
	if toolExec.calls[0] != `read:{"path":"README.md"}` {
		t.Fatalf("unexpected tool call: %s", toolExec.calls[0])
	}
	if len(events) != 2 || events[0].Kind != "tool_call" || events[1].Kind != "tool_result" {
		t.Fatalf("unexpected tool events: %+v", events)
	}
}

func TestAgentRunTurn_ToolErrorInjectedAsToolContent(t *testing.T) {
	client := &fakeStreamingClient{messages: []openrouter.Message{
		{
			Role: "assistant",
			ToolCalls: []openrouter.ToolCall{{
				ID:   "call_1",
				Type: "function",
				Function: openrouter.ToolFunctionCall{
					Name:      "bash",
					Arguments: `{"command":"false"}`,
				},
			}},
		},
		{Role: "assistant", Content: "I handled the error"},
	}}
	toolExec := &fakeToolExecutor{
		results: map[string]string{"bash": "stderr output"},
		errs:    map[string]error{"bash": fmt.Errorf("command failed")},
	}
	a := NewWithTools(client, toolExec)

	newMessages, err := a.RunTurn(context.Background(), "openai/gpt-4o-mini", nil, "run failing command", TurnCallbacks{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if newMessages[len(newMessages)-1].Content != "I handled the error" {
		t.Fatalf("unexpected final: %q", newMessages[len(newMessages)-1].Content)
	}
	if newMessages[2].Role != "tool" || !strings.Contains(newMessages[2].Content, "Tool error") {
		t.Fatalf("expected tool error in tool message, got %+v", newMessages[2])
	}
}

func TestAgentRunTurn_Validation(t *testing.T) {
	a := NewWithTools(nil, &fakeToolExecutor{})
	if _, err := a.RunTurn(context.Background(), "model", nil, "hello", TurnCallbacks{}); err == nil {
		t.Fatalf("expected nil client error")
	}

	a = NewWithTools(&fakeStreamingClient{messages: []openrouter.Message{{Role: "assistant", Content: "ok"}}}, nil)
	if _, err := a.RunTurn(context.Background(), "model", nil, "   ", TurnCallbacks{}); err == nil {
		t.Fatalf("expected empty input error")
	}
}
