package main

import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"pigeon/internal/agent"
	"pigeon/internal/app"
	"pigeon/internal/config"
	luaext "pigeon/internal/extensions/lua"
	"pigeon/internal/provider/openrouter"
	"pigeon/internal/resources"
	"pigeon/internal/session"
	"pigeon/internal/tui"
)

func main() {
	model := flag.String("model", "openai/gpt-4o-mini", "OpenRouter model id")
	systemFlag := flag.String("system", "", "system prompt (overrides ~/.config/pigeon/system.md and .pigeon/system.md)")
	flag.Parse()

	systemPrompt := config.ResolveSystemPrompt(*systemFlag)
	settings := config.LoadSettings()

	apiKey, err := app.ResolveOpenRouterAPIKey(os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pigeon: %v\n", err)
		os.Exit(1)
	}

	client := openrouter.NewClient(apiKey, nil)
	ag := agent.New(client)

	sessionManager := session.NewManager("")
	if _, err := sessionManager.PruneEmptySessions(); err != nil {
		fmt.Fprintf(os.Stderr, "pigeon: warning: failed to prune empty sessions: %v\n", err)
	}
	sessionID, err := sessionManager.NewSession()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pigeon: warning: failed to initialize session file: %v\n", err)
		sessionID = ""
	}

	if sessionID != "" {
		if err := sessionManager.SetSessionModel(sessionID, *model); err != nil {
			fmt.Fprintf(os.Stderr, "pigeon: warning: failed to persist initial model: %v\n", err)
		}
	}

	reg, err := resources.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pigeon: warning: failed to load resources: %v\n", err)
	}

	statusCh := make(chan luaext.StatusUpdate, 64)
	runtime := luaext.NewRuntime(statusCh)
	if reg != nil {
		for _, ext := range reg.ListExtensionPaths() {
			if err := runtime.Load(ext.Name, ext.Path); err != nil {
				fmt.Fprintf(os.Stderr, "pigeon: warning: extension %s: %v\n", ext.Name, err)
			}
		}
	}

	m := tui.NewModel(ag, client, *model, sessionManager, sessionID, reg, runtime, statusCh, settings, systemPrompt)
	p := tea.NewProgram(m, tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "pigeon: runtime error: %v\n", err)
		os.Exit(1)
	}
}
