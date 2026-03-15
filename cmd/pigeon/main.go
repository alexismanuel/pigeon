package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"

	tea "github.com/charmbracelet/bubbletea"

	"pigeon/internal/agent"
	"pigeon/internal/app"
	"pigeon/internal/auth"
	"pigeon/internal/config"
	anthropicclient "pigeon/internal/provider/anthropic"
	luaext "pigeon/internal/extensions/lua"
	"pigeon/internal/permission"
	"pigeon/internal/resources"
	"pigeon/internal/session"
	"pigeon/internal/tools"
	"pigeon/internal/tui"
)

func main() {
	// ── sub-commands ──────────────────────────────────────────────────────────
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "login":
			runLogin()
			return
		case "logout":
			runLogout()
			return
		}
	}

	// ── chat flags ────────────────────────────────────────────────────────────
	model := flag.String("model", "", "Model ID (e.g. claude-sonnet-4-6, openai/gpt-4o-mini)")
	systemFlag := flag.String("system", "", "system prompt (overrides ~/.config/pigeon/system.md and .pigeon/system.md)")
	flag.Parse()

	systemPrompt := config.ResolveSystemPrompt(*systemFlag)
	settings := config.LoadSettings()

	// Build multi-provider (OpenRouter + Anthropic + LM Studio as available).
	mp, err := app.BuildProviders(os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pigeon: %v\n", err)
		os.Exit(1)
	}

	// Choose a sensible default model.
	modelName := *model
	if modelName == "" {
		modelName = defaultModel()
	}

	// Build the permission service and wire it into the executor.
	workingDir, _ := os.Getwd()
	permService := permission.NewService(
		workingDir,
		settings.Permissions.SkipRequests,
		settings.Permissions.AllowedTools,
		settings.Permissions.BashDenyPatterns,
	)
	executor := tools.NewExecutorWithPermissions(permService)
	ag := agent.NewWithTools(mp, executor)

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
		if err := sessionManager.SetSessionModel(sessionID, modelName); err != nil {
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

	// onProviderLogin is called after a successful in-TUI OAuth login.
	// It hot-adds the freshly authenticated provider to the multi-provider so
	// models appear in the picker immediately without a restart.
	onProviderLogin := func(providerID string) {
		switch providerID {
		case "anthropic":
			token, err := auth.GetAnthropicToken()
			if err == nil && token != "" {
				mp.Add("anthropic", anthropicclient.NewClient(token, nil))
			}
		}
	}

	m := tui.NewModel(ag, mp, modelName, sessionManager, sessionID, reg, runtime, statusCh, settings, permService, onProviderLogin, systemPrompt)
	p := tea.NewProgram(m, tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "pigeon: runtime error: %v\n", err)
		os.Exit(1)
	}
}

// defaultModel returns a sensible default model depending on which providers
// are configured.
func defaultModel() string {
	if key := os.Getenv(app.AnthropicAPIKeyEnv); key != "" {
		return "claude-sonnet-4-6"
	}
	// Check for stored OAuth credentials.
	if token, err := auth.GetAnthropicToken(); err == nil && token != "" {
		return "claude-sonnet-4-6"
	}
	if key := os.Getenv(app.OpenRouterAPIKeyEnv); key != "" {
		return "openai/gpt-4o-mini"
	}
	// LM Studio default.
	return "qwen3.5-27B"
}

// ─── login / logout sub-commands ────────────────────────────────────────────

func runLogin() {
	fmt.Println("Opening browser for Anthropic OAuth login…")
	fmt.Println("(Claude Pro/Max subscription required)")
	fmt.Println()

	creds, err := auth.Login(context.Background(), auth.LoginCallbacks{
		OnAuthURL: func(authURL string) {
			fmt.Printf("Authorization URL:\n  %s\n\n", authURL)
			openBrowser(authURL)
		},
		OnProgress: func(msg string) {
			fmt.Println(msg)
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "pigeon login: %v\n", err)
		os.Exit(1)
	}

	if err := auth.SetAnthropicOAuth(creds); err != nil {
		fmt.Fprintf(os.Stderr, "pigeon login: save credentials: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("✓ Logged in to Anthropic. Credentials saved to ~/.config/pigeon/auth.json")
}

func runLogout() {
	if err := auth.RemoveProvider("anthropic"); err != nil {
		fmt.Fprintf(os.Stderr, "pigeon logout: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✓ Logged out from Anthropic.")
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
