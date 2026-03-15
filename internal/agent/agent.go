package agent

import (
	"context"
	"fmt"
	"strings"

	"pigeon/internal/provider/openrouter"
	"pigeon/internal/tools"
)

const maxToolRounds = 12

type StreamingClient interface {
	StreamChatCompletion(
		ctx context.Context,
		model string,
		messages []openrouter.Message,
		tools []openrouter.ToolDefinition,
		onEvent openrouter.StreamHandler,
	) (openrouter.Message, error)
}

type toolExecutor interface {
	Definitions() []openrouter.ToolDefinition
	Execute(ctx context.Context, name, argumentsJSON string) (string, error)
}

type ToolEvent struct {
	Kind      string
	ToolName  string
	Arguments string
	Result    string
	Err       error
}

type TurnCallbacks struct {
	OnToken        func(string)
	OnThinkingToken func(string) // reasoning/thinking tokens from models that expose them
	OnToolEvent    func(ToolEvent)
	// BeforeToolCall fires synchronously before each tool execution.
	// Returning true blocks the call; the agent substitutes a canned
	// "blocked by extension" result so the model can continue cleanly.
	BeforeToolCall func(name, args string) bool
}

type Agent struct {
	client StreamingClient
	tools  toolExecutor
}

func New(client StreamingClient) *Agent {
	return &Agent{client: client, tools: tools.NewExecutor()}
}

func NewWithTools(client StreamingClient, executor toolExecutor) *Agent {
	if executor == nil {
		executor = tools.NewExecutor()
	}
	return &Agent{client: client, tools: executor}
}

func (a *Agent) RunTurn(ctx context.Context, model string, history []openrouter.Message, userInput string, cb TurnCallbacks) ([]openrouter.Message, error) {
	if a.client == nil {
		return nil, fmt.Errorf("streaming client is required")
	}
	if a.tools == nil {
		return nil, fmt.Errorf("tool executor is required")
	}
	if strings.TrimSpace(userInput) == "" {
		return nil, fmt.Errorf("input cannot be empty")
	}
	if cb.OnToken == nil {
		cb.OnToken = func(string) {}
	}
	if cb.OnThinkingToken == nil {
		cb.OnThinkingToken = func(string) {}
	}
	if cb.OnToolEvent == nil {
		cb.OnToolEvent = func(ToolEvent) {}
	}
	if cb.BeforeToolCall == nil {
		cb.BeforeToolCall = func(string, string) bool { return false }
	}

	messages := append([]openrouter.Message{}, history...)
	messages = append(messages, openrouter.Message{Role: "user", Content: userInput})
	newMessages := []openrouter.Message{{Role: "user", Content: userInput}}
	toolDefs := a.tools.Definitions()

	for round := 0; round < maxToolRounds; round++ {
		assistantMsg, err := a.client.StreamChatCompletion(ctx, model, messages, toolDefs, func(event openrouter.StreamEvent) {
			if event.Delta.Reasoning != "" {
				cb.OnThinkingToken(event.Delta.Reasoning)
			}
			if event.Delta.Content != "" {
				cb.OnToken(event.Delta.Content)
			}
		})
		if err != nil {
			return nil, err
		}

		messages = append(messages, assistantMsg)
		newMessages = append(newMessages, assistantMsg)

		if len(assistantMsg.ToolCalls) == 0 {
			return newMessages, nil
		}

		for _, toolCall := range assistantMsg.ToolCalls {
			name := toolCall.Function.Name
			args := toolCall.Function.Arguments

			cb.OnToolEvent(ToolEvent{
				Kind:      "tool_call",
				ToolName:  name,
				Arguments: args,
			})

			var toolResult string
			var toolErr error
			if cb.BeforeToolCall(name, args) {
				// Blocked by extension — give the model a clear signal.
				toolResult = fmt.Sprintf("[tool call '%s' blocked by extension]", name)
			} else {
				toolResult, toolErr = a.tools.Execute(ctx, name, args)
				if toolErr != nil {
					if strings.TrimSpace(toolResult) == "" {
						toolResult = "tool error: " + toolErr.Error()
					} else {
						toolResult = toolResult + "\n\nTool error: " + toolErr.Error()
					}
				}
			}

			cb.OnToolEvent(ToolEvent{
				Kind:     "tool_result",
				ToolName: name,
				Result:   toolResult,
				Err:      toolErr,
			})

			toolMsg := openrouter.Message{
				Role:       "tool",
				ToolCallID: toolCall.ID,
				Name:       toolCall.Function.Name,
				Content:    toolResult,
			}
			messages = append(messages, toolMsg)
			newMessages = append(newMessages, toolMsg)
		}
	}

	return nil, fmt.Errorf("tool loop exceeded maximum rounds (%d)", maxToolRounds)
}
