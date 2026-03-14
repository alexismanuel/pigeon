// Package config resolves pigeon's on-disk configuration.
package config

import (
	_ "embed"
	"os"
	"path/filepath"
	"strings"
)

const systemFile = "system.md"

//go:embed default-system-prompt.md
var defaultSystemPrompt string

// ResolveSystemPrompt returns the system prompt to use for the session.
// Priority (highest first):
//  1. cliFlag — the value of the -system CLI flag
//  2. .pigeon/system.md in the current working directory (project-local)
//  3. ~/.config/pigeon/system.md (user default)
//  4. compiled-in default (docs/system-prompt.md)
//
// Never returns an error; missing files are silently skipped.
func ResolveSystemPrompt(cliFlag string) string {
	if s := strings.TrimSpace(cliFlag); s != "" {
		return s
	}
	if s := readFile(projectSystemPath()); s != "" {
		return s
	}
	if s := readFile(userSystemPath()); s != "" {
		return s
	}
	return strings.TrimSpace(defaultSystemPrompt)
}

// DefaultSystemPrompt returns the compiled-in default system prompt.
func DefaultSystemPrompt() string {
	return strings.TrimSpace(defaultSystemPrompt)
}

// UserSystemPath returns ~/.config/pigeon/system.md.
func UserSystemPath() string { return userSystemPath() }

// ProjectSystemPath returns .pigeon/system.md relative to cwd.
func ProjectSystemPath() string { return projectSystemPath() }

func userSystemPath() string {
	base, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "pigeon", systemFile)
}

func projectSystemPath() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return filepath.Join(cwd, ".pigeon", systemFile)
}

func readFile(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
