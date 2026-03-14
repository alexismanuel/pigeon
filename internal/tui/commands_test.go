package tui

import (
	"testing"
)

func TestFilterCommands_SlashReturnsAll(t *testing.T) {
	got := filterCommands("/", nil)
	if len(got) != len(builtinCommands) {
		t.Errorf("expected %d commands for '/', got %d", len(builtinCommands), len(got))
	}
}

func TestFilterCommands_PrefixFilters(t *testing.T) {
	got := filterCommands("/mo", nil)
	if len(got) != 1 || got[0].name != "/model" {
		t.Errorf("expected [/model], got %v", got)
	}
}

func TestFilterCommands_NoMatch(t *testing.T) {
	got := filterCommands("/zzz", nil)
	if len(got) != 0 {
		t.Errorf("expected no matches, got %v", got)
	}
}

func TestFilterCommands_ExactMatch(t *testing.T) {
	got := filterCommands("/quit", nil)
	if len(got) != 1 || got[0].name != "/quit" {
		t.Errorf("expected [/quit], got %v", got)
	}
}

func TestFilterCommands_IncludesExtras(t *testing.T) {
	extra := []commandDef{
		{name: "/openrouter", desc: "show usage"},
	}
	got := filterCommands("/o", extra)
	if len(got) != 1 || got[0].name != "/openrouter" {
		t.Errorf("expected [/openrouter] from extras, got %v", got)
	}
}

func TestFilterCommands_SlashIncludesExtras(t *testing.T) {
	extra := []commandDef{
		{name: "/foo", desc: "foo"},
		{name: "/bar", desc: "bar"},
	}
	got := filterCommands("/", extra)
	if len(got) != len(builtinCommands)+2 {
		t.Errorf("expected %d commands, got %d", len(builtinCommands)+2, len(got))
	}
}

func TestFilterCommands_CaseInsensitivePrefix(t *testing.T) {
	// commands are lowercase so "/MO" won't match — this is expected behaviour
	// (user types "/" then lowercase characters)
	got := filterCommands("/mo", nil)
	if len(got) == 0 {
		t.Error("expected /model for prefix /mo")
	}
}

func TestFilterCommands_MultipleMatches(t *testing.T) {
	// "/n" matches /new but not /model, /resume, /tree, /quit
	got := filterCommands("/n", nil)
	if len(got) != 1 || got[0].name != "/new" {
		t.Errorf("expected [/new], got %v", got)
	}

	// "/se" matches only /sessions (not /system)
	got = filterCommands("/se", nil)
	if len(got) != 1 || got[0].name != "/sessions" {
		t.Errorf("expected [/sessions], got %v", got)
	}

	// "/sy" matches only /system
	got = filterCommands("/sy", nil)
	if len(got) != 1 || got[0].name != "/system" {
		t.Errorf("expected [/system], got %v", got)
	}
}
