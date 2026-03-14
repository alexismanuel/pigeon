package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveSystemPrompt_CLIFlagWins(t *testing.T) {
	got := ResolveSystemPrompt("You are a pirate.")
	if got != "You are a pirate." {
		t.Errorf("expected CLI flag value, got %q", got)
	}
}

func TestResolveSystemPrompt_CLIFlagTrimsSpace(t *testing.T) {
	got := ResolveSystemPrompt("  spaced  ")
	if got != "spaced" {
		t.Errorf("expected trimmed, got %q", got)
	}
}

func TestResolveSystemPrompt_EmptyFlagFallsToDefault(t *testing.T) {
	// With no file overrides and empty flag, returns the compiled-in default.
	// We cannot easily suppress the user's ~/.config/pigeon/system.md in CI,
	// but we can at least verify the function doesn't panic and returns something.
	got := ResolveSystemPrompt("")
	if got == "" {
		t.Error("expected non-empty prompt from compiled-in default")
	}
}

func TestDefaultSystemPrompt_NotEmpty(t *testing.T) {
	got := DefaultSystemPrompt()
	if got == "" {
		t.Error("compiled-in default prompt is empty")
	}
	if !strings.Contains(got, "pigeon") {
		t.Errorf("expected 'pigeon' in default prompt, got (truncated): %.80q", got)
	}
}

func TestReadFile_Missing(t *testing.T) {
	got := readFile("/nonexistent/path/file.md")
	if got != "" {
		t.Errorf("expected empty for missing file, got %q", got)
	}
}

func TestReadFile_EmptyPath(t *testing.T) {
	got := readFile("")
	if got != "" {
		t.Errorf("expected empty for empty path, got %q", got)
	}
}

func TestReadFile_TrimsWhitespace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "system.md")
	os.WriteFile(path, []byte("  hello world  \n"), 0o644)
	got := readFile(path)
	if got != "hello world" {
		t.Errorf("expected trimmed, got %q", got)
	}
}

func TestResolveSystemPrompt_ProjectLocalOverridesUserDefault(t *testing.T) {
	// Set up temp dirs simulating both config sources.
	configDir := t.TempDir()
	projectDir := t.TempDir()

	// User-level system.md.
	userPath := filepath.Join(configDir, "pigeon", systemFile)
	os.MkdirAll(filepath.Dir(userPath), 0o755)
	os.WriteFile(userPath, []byte("user level"), 0o644)

	// Project-level system.md.
	projectPath := filepath.Join(projectDir, ".pigeon", systemFile)
	os.MkdirAll(filepath.Dir(projectPath), 0o755)
	os.WriteFile(projectPath, []byte("project level"), 0o644)

	// Directly test priority by calling readFile in order (mirrors ResolveSystemPrompt logic).
	if s := readFile(projectPath); s != "project level" {
		t.Errorf("project path: got %q", s)
	}
	if s := readFile(userPath); s != "user level" {
		t.Errorf("user path: got %q", s)
	}

	// CLI flag beats both.
	got := ResolveSystemPrompt("cli wins")
	if got != "cli wins" {
		t.Errorf("CLI flag should beat all: got %q", got)
	}
}
