package agent

import (
	"context"
	"fmt"
	"strings"

	"pigeon/internal/provider/openrouter"
)

type StreamingClient interface {
	StreamChatCompletion(ctx context.Context, model string, messages []openrouter.Message, onEvent openrouter.StreamHandler) error
}

type Agent struct {
	client StreamingClient
}

func New(client StreamingClient) *Agent {
	return &Agent{client: client}
}

func (a *Agent) RunTurn(ctx context.Context, model string, userInput string, onToken func(string)) (string, error) {
	if a.client == nil {
		return "", fmt.Errorf("streaming client is required")
	}
	if strings.TrimSpace(userInput) == "" {
		return "", fmt.Errorf("input cannot be empty")
	}
	if onToken == nil {
		onToken = func(string) {}
	}

	messages := []openrouter.Message{{Role: "user", Content: userInput}}
	var builder strings.Builder
	err := a.client.StreamChatCompletion(ctx, model, messages, func(event openrouter.StreamEvent) {
		if event.Delta.Content != "" {
			builder.WriteString(event.Delta.Content)
			onToken(event.Delta.Content)
		}
	})
	if err != nil {
		return "", err
	}
	return builder.String(), nil
}
