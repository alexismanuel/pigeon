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
	Keybindings      Keybindings `json:"keybindings"`
	// CollapseThinking controls whether thinking blocks are collapsed after a
	// turn completes.  Defaults to false (thinking shown in full).
	CollapseThinking bool        `json:"collapse_thinking"`
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
		},
	}
}

// rawSettings mirrors Settings but uses *bool for fields where false is a
// meaningful override (not just a zero value meaning "unset").
type rawSettings struct {
	Keybindings      Keybindings `json:"keybindings"`
	CollapseThinking *bool       `json:"collapse_thinking"`
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

	// Merge bool: pointer distinguishes explicit false from "not set".
	if raw.CollapseThinking != nil {
		s.CollapseThinking = *raw.CollapseThinking
	}

	return s
}

// SettingsPath returns the path to the user settings file.
func SettingsPath() string { return settingsPath() }

func settingsPath() string {
	base, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "pigeon", settingsFile)
}
