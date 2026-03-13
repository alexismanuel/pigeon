package main

import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"pigeon/internal/agent"
	"pigeon/internal/app"
	"pigeon/internal/provider/openrouter"
	"pigeon/internal/tui"
)

func main() {
	model := flag.String("model", "openai/gpt-4o-mini", "OpenRouter model id")
	flag.Parse()

	apiKey, err := app.ResolveOpenRouterAPIKey(os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pigeon: %v\n", err)
		os.Exit(1)
	}

	client := openrouter.NewClient(apiKey, nil)
	ag := agent.New(client)
	m := tui.NewModel(ag, *model)

	p := tea.NewProgram(m)
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "pigeon: runtime error: %v\n", err)
		os.Exit(1)
	}
}
