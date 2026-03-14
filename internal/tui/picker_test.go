package tui

import (
	"testing"

	"pigeon/internal/provider/openrouter"
)

var testModels = []openrouter.ModelInfo{
	{ID: "anthropic/claude-3-5-sonnet", Name: "Claude 3.5 Sonnet", ContextLength: 200000},
	{ID: "openai/gpt-4o", Name: "GPT-4o", ContextLength: 128000},
	{ID: "openai/gpt-4o-mini", Name: "GPT-4o Mini", ContextLength: 128000},
	{ID: "google/gemini-pro", Name: "Gemini Pro", ContextLength: 32000},
	{ID: "mistralai/mistral-7b", Name: "Mistral 7B", ContextLength: 8000},
}

func TestFilterModels_EmptyReturnsAll(t *testing.T) {
	got := filterModels(testModels, "")
	if len(got) != len(testModels) {
		t.Errorf("expected %d models, got %d", len(testModels), len(got))
	}
}

func TestFilterModels_MatchByName(t *testing.T) {
	got := filterModels(testModels, "claude")
	if len(got) != 1 || got[0].ID != "anthropic/claude-3-5-sonnet" {
		t.Errorf("expected claude model, got %v", got)
	}
}

func TestFilterModels_MatchByID(t *testing.T) {
	got := filterModels(testModels, "openai/")
	if len(got) != 2 {
		t.Errorf("expected 2 openai models, got %d", len(got))
	}
}

func TestFilterModels_CaseInsensitive(t *testing.T) {
	gotLower := filterModels(testModels, "gpt")
	gotUpper := filterModels(testModels, "GPT")
	if len(gotLower) != len(gotUpper) {
		t.Errorf("case sensitivity mismatch: lower=%d upper=%d", len(gotLower), len(gotUpper))
	}
	if len(gotLower) == 0 {
		t.Error("expected at least one GPT model")
	}
}

func TestFilterModels_NoMatch(t *testing.T) {
	got := filterModels(testModels, "zzznomatch")
	if len(got) != 0 {
		t.Errorf("expected no matches, got %d", len(got))
	}
}

func TestFilterModels_PartialNameMatch(t *testing.T) {
	// "4o-mini" is specific enough to match only gpt-4o-mini
	got := filterModels(testModels, "4o-mini")
	if len(got) != 1 || got[0].ID != "openai/gpt-4o-mini" {
		t.Errorf("expected gpt-4o-mini, got %v", got)
	}

	// "mini" also matches gemini (substring in ID), so expect 2 results
	got = filterModels(testModels, "mini")
	if len(got) != 2 {
		t.Errorf("'mini' should match gpt-4o-mini and gemini, got %d: %v", len(got), got)
	}
}

func TestFilterModels_WhitespaceOnlyIsAll(t *testing.T) {
	got := filterModels(testModels, "   ")
	if len(got) != len(testModels) {
		t.Errorf("whitespace-only query should return all; got %d", len(got))
	}
}

// ── truncStr ──────────────────────────────────────────────────────────────────

func TestTruncStr_ShortPassesThrough(t *testing.T) {
	s := "hello"
	got := truncStr(s, 10)
	if got != s {
		t.Errorf("expected %q, got %q", s, got)
	}
}

func TestTruncStr_ExactLengthPassesThrough(t *testing.T) {
	s := "hello"
	got := truncStr(s, 5)
	if got != s {
		t.Errorf("expected %q unchanged, got %q", s, got)
	}
}

func TestTruncStr_LongIsTruncated(t *testing.T) {
	s := "hello world"
	got := truncStr(s, 8)
	if len([]rune(got)) != 8 {
		t.Errorf("expected length 8, got %d (%q)", len([]rune(got)), got)
	}
	// last rune should be the ellipsis
	runes := []rune(got)
	if runes[len(runes)-1] != '…' {
		t.Errorf("expected trailing ellipsis, got %q", got)
	}
}

func TestTruncStr_MultiByte(t *testing.T) {
	// "日本語テスト" = 6 runes
	s := "日本語テスト"
	got := truncStr(s, 4)
	runes := []rune(got)
	if len(runes) != 4 {
		t.Errorf("expected 4 runes, got %d", len(runes))
	}
	if runes[3] != '…' {
		t.Errorf("expected ellipsis at position 3, got %q", string(runes[3]))
	}
}
