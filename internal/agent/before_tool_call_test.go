package agent

import (
	"context"
	"strings"
	"testing"

	"pigeon/internal/provider/openrouter"
)

func toolCallClient(name, args string, finalContent string) *fakeStreamingClient {
	return &fakeStreamingClient{messages: []openrouter.Message{
		{
			Role: "assistant",
			ToolCalls: []openrouter.ToolCall{{
				ID:   "call_1",
				Type: "function",
				Function: openrouter.ToolFunctionCall{Name: name, Arguments: args},
			}},
		},
		{Role: "assistant", Content: finalContent},
	}}
}

func TestBeforeToolCall_Blocks(t *testing.T) {
	client := toolCallClient("bash", `{"command":"echo hi"}`, "ok, blocked")
	toolExec := &fakeToolExecutor{results: map[string]string{}}
	a := NewWithTools(client, toolExec)

	var beforeCount int
	var resultEvents []ToolEvent
	msgs, err := a.RunTurn(context.Background(), "openai/gpt-4o-mini", nil, "run something", TurnCallbacks{
		BeforeToolCall: func(name, _ string) bool {
			beforeCount++
			return true // block
		},
		OnToolEvent: func(evt ToolEvent) {
			if evt.Kind == "tool_result" {
				resultEvents = append(resultEvents, evt)
			}
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// BeforeToolCall fired once.
	if beforeCount != 1 {
		t.Errorf("BeforeToolCall called %d times, want 1", beforeCount)
	}
	// Tool executor was never invoked.
	if len(toolExec.calls) != 0 {
		t.Errorf("tool executor should not run when blocked, got calls %v", toolExec.calls)
	}
	// OnToolEvent still fires for tool_result so the TUI can render it.
	if len(resultEvents) != 1 {
		t.Errorf("expected 1 tool_result event, got %d", len(resultEvents))
	}
	// The tool message in the conversation must mention "blocked".
	found := false
	for _, m := range msgs {
		if m.Role == "tool" && strings.Contains(m.Content, "blocked") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'blocked' in tool message: %+v", msgs)
	}
}

func TestBeforeToolCall_Allows(t *testing.T) {
	client := toolCallClient("read", `{"path":"README.md"}`, "done")
	toolExec := &fakeToolExecutor{results: map[string]string{"read": "file contents"}}
	a := NewWithTools(client, toolExec)

	var called bool
	_, err := a.RunTurn(context.Background(), "model", nil, "read file", TurnCallbacks{
		BeforeToolCall: func(name, _ string) bool {
			called = true
			return false // allow
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("BeforeToolCall should have been called")
	}
	if len(toolExec.calls) == 0 {
		t.Error("tool should have executed when not blocked")
	}
}

func TestBeforeToolCall_NilDefaultsToAllow(t *testing.T) {
	client := toolCallClient("read", `{"path":"x"}`, "ok")
	toolExec := &fakeToolExecutor{results: map[string]string{"read": "data"}}
	a := NewWithTools(client, toolExec)

	// nil BeforeToolCall — must not panic, tool must execute.
	if _, err := a.RunTurn(context.Background(), "model", nil, "go", TurnCallbacks{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(toolExec.calls) == 0 {
		t.Error("tool should execute with nil BeforeToolCall")
	}
}

func TestBeforeToolCall_MultipleTools_BlockOne(t *testing.T) {
	// Two tool calls in the same round; block only bash, allow read.
	client := &fakeStreamingClient{messages: []openrouter.Message{
		{
			Role: "assistant",
			ToolCalls: []openrouter.ToolCall{
				{ID: "c1", Type: "function", Function: openrouter.ToolFunctionCall{Name: "bash", Arguments: `{"command":"ls"}`}},
				{ID: "c2", Type: "function", Function: openrouter.ToolFunctionCall{Name: "read", Arguments: `{"path":"f"}`}},
			},
		},
		{Role: "assistant", Content: "done"},
	}}
	toolExec := &fakeToolExecutor{results: map[string]string{"read": "ok"}}
	a := NewWithTools(client, toolExec)

	msgs, err := a.RunTurn(context.Background(), "model", nil, "two tools", TurnCallbacks{
		BeforeToolCall: func(name, _ string) bool {
			return name == "bash"
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Only read should appear in executor calls.
	for _, c := range toolExec.calls {
		if strings.HasPrefix(c, "bash:") {
			t.Errorf("bash should have been blocked, but executor was called with %q", c)
		}
	}

	// Both tool messages exist in final msgs.
	toolMsgs := 0
	for _, m := range msgs {
		if m.Role == "tool" {
			toolMsgs++
		}
	}
	if toolMsgs != 2 {
		t.Errorf("expected 2 tool messages, got %d", toolMsgs)
	}
}
