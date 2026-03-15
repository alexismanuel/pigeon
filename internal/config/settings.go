package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const settingsFile = "settings.json"

// Settings holds user-configurable pigeon settings loaded from
// ~/.config/pigeon/settings.json.  Every field has a sensible default so the
// file is entirely optional.
type Settings struct {
	Keybindings Keybindings `json:"keybindings"`
	// CollapseThinking controls whether thinking blocks are collapsed after a
	// turn completes.  Defaults to false (thinking shown in full).
	CollapseThinking bool `json:"collapse_thinking"`
	// Permissions configures the tool-call permission system.
	Permissions PermissionConfig `json:"permissions"`
}

// PermissionConfig controls which tool calls require explicit user approval.
type PermissionConfig struct {
	// SkipRequests disables all permission checks when true — every tool call
	// is auto-approved without prompting.  Useful for non-interactive or fully
	// trusted environments.
	SkipRequests bool `json:"skip_requests"`
	// AllowedTools is a list of tool names or "toolName:action" pairs that are
	// auto-approved without prompting.  Examples:
	//   "read"          — all read operations
	//   "bash:execute"  — all bash commands (same as "bash")
	AllowedTools []string `json:"allowed_tools"`
	// BashDenyPatterns is a list of glob patterns matched against bash commands.
	// Commands that match are automatically denied without prompting.
	// Use "*" as a wildcard.  Examples:
	//   "rm *"    — deny any rm invocation
	//   "sudo *"  — deny all sudo usage
	//   "git push" — deny exactly "git push" (and "git push <anything>")
	BashDenyPatterns []string `json:"bash_deny_patterns"`
}

// Keybindings holds the key sequences for pigeon's chat shortcuts.
// Values must be BubbleTea key strings (e.g. "ctrl+c", "alt+esc").
type Keybindings struct {
	// ClearInput clears the text-input field when it is non-empty.
	ClearInput string `json:"clear_input"`
	// Quit exits pigeon.
	Quit string `json:"quit"`
	// CancelTurn aborts the currently running assistant turn.
	CancelTurn string `json:"cancel_turn"`
	// ToggleThinking collapses or expands all thinking blocks in the current session.
	ToggleThinking string `json:"toggle_thinking"`
	// ToggleTools collapses or expands all tool-result blocks in the current session.
	ToggleTools string `json:"toggle_tools"`
}

// defaults returns the built-in settings used when no settings file exists
// or when individual fields are left blank/unset.
func defaults() Settings {
	return Settings{
		CollapseThinking: false,
		Keybindings: Keybindings{
			ClearInput:     "alt+c",
			Quit:           "alt+q",
			CancelTurn:     "alt+esc",
			ToggleThinking: "alt+t",
			ToggleTools:    "alt+r",
		},
	}
}

// rawSettings mirrors Settings but uses pointer types for booleans where
// false is a meaningful override (not just the zero value meaning "unset").
type rawSettings struct {
	Keybindings      Keybindings      `json:"keybindings"`
	CollapseThinking *bool            `json:"collapse_thinking"`
	Permissions      *PermissionConfig `json:"permissions"`
}

// LoadSettings reads ~/.config/pigeon/settings.json and merges it over the
// built-in defaults.  Missing keys in the file keep their default values.
// Any file-system or parse error is silently ignored and the defaults are
// returned.
func LoadSettings() Settings {
	s := defaults()

	path := settingsPath()
	if path == "" {
		return s
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return s // file absent or unreadable — use defaults
	}

	var raw rawSettings
	if err := json.Unmarshal(data, &raw); err != nil {
		return s // malformed JSON — use defaults
	}

	// Merge keybindings: only override when the JSON field is non-empty.
	if raw.Keybindings.ClearInput != "" {
		s.Keybindings.ClearInput = raw.Keybindings.ClearInput
	}
	if raw.Keybindings.Quit != "" {
		s.Keybindings.Quit = raw.Keybindings.Quit
	}
	if raw.Keybindings.CancelTurn != "" {
		s.Keybindings.CancelTurn = raw.Keybindings.CancelTurn
	}
	if raw.Keybindings.ToggleThinking != "" {
		s.Keybindings.ToggleThinking = raw.Keybindings.ToggleThinking
	}
	if raw.Keybindings.ToggleTools != "" {
		s.Keybindings.ToggleTools = raw.Keybindings.ToggleTools
	}

	// Merge bool: pointer distinguishes explicit false from "not set".
	if raw.CollapseThinking != nil {
		s.CollapseThinking = *raw.CollapseThinking
	}

	// Merge permissions block when present.
	if raw.Permissions != nil {
		s.Permissions = *raw.Permissions
	}

	return s
}

// SettingsPath returns the path to the user settings file.
func SettingsPath() string { return settingsPath() }

func settingsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "pigeon", settingsFile)
}
