package resources_test

import (
	"os"
	"path/filepath"
	"testing"

	"pigeon/internal/resources"
)

// writeFile creates a file and all parent dirs in one call.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// ── skills ─────────────────────────────────────────────────────────────────────

func TestLoadSkills_GlobalOnly(t *testing.T) {
	global := t.TempDir()
	writeFile(t, filepath.Join(global, "skills", "go-dev", "SKILL.md"), "# Go dev skill")

	r, err := resources.LoadFrom(global, "")
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	skills := r.ListSkills()
	if len(skills) != 1 {
		t.Fatalf("want 1 skill, got %d", len(skills))
	}
	if skills[0].Name != "go-dev" {
		t.Errorf("want name=go-dev, got %q", skills[0].Name)
	}
	if skills[0].Content != "# Go dev skill" {
		t.Errorf("unexpected content: %q", skills[0].Content)
	}
}

func TestLoadSkills_LocalOverridesGlobal(t *testing.T) {
	global := t.TempDir()
	local := t.TempDir()
	writeFile(t, filepath.Join(global, "skills", "review", "SKILL.md"), "global review")
	writeFile(t, filepath.Join(local, "skills", "review", "SKILL.md"), "local review")

	r, err := resources.LoadFrom(global, local)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	skill, ok := r.GetSkill("review")
	if !ok {
		t.Fatal("expected skill 'review' to exist")
	}
	if skill.Content != "local review" {
		t.Errorf("local should override global: got %q", skill.Content)
	}
}

func TestLoadSkills_CaseInsensitiveLookup(t *testing.T) {
	global := t.TempDir()
	writeFile(t, filepath.Join(global, "skills", "MySkill", "SKILL.md"), "content")

	r, _ := resources.LoadFrom(global, "")

	if _, ok := r.GetSkill("myskill"); !ok {
		t.Error("expected case-insensitive lookup to find 'myskill'")
	}
	if _, ok := r.GetSkill("MYSKILL"); !ok {
		t.Error("expected case-insensitive lookup to find 'MYSKILL'")
	}
}

func TestLoadSkills_MissingSkillMdSkipped(t *testing.T) {
	global := t.TempDir()
	// directory exists but has no SKILL.md
	if err := os.MkdirAll(filepath.Join(global, "skills", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	r, _ := resources.LoadFrom(global, "")
	if len(r.ListSkills()) != 0 {
		t.Error("skill dir without SKILL.md should be ignored")
	}
}

func TestLoadSkills_MultipleSkillsSorted(t *testing.T) {
	global := t.TempDir()
	writeFile(t, filepath.Join(global, "skills", "zebra", "SKILL.md"), "z")
	writeFile(t, filepath.Join(global, "skills", "alpha", "SKILL.md"), "a")
	writeFile(t, filepath.Join(global, "skills", "middle", "SKILL.md"), "m")

	r, _ := resources.LoadFrom(global, "")
	skills := r.ListSkills()

	names := make([]string, len(skills))
	for i, s := range skills {
		names[i] = s.Name
	}
	want := []string{"alpha", "middle", "zebra"}
	for i, w := range want {
		if names[i] != w {
			t.Errorf("index %d: want %q, got %q", i, w, names[i])
		}
	}
}

// ── prompts ────────────────────────────────────────────────────────────────────

func TestLoadPrompts_GlobalOnly(t *testing.T) {
	global := t.TempDir()
	writeFile(t, filepath.Join(global, "prompts", "review.md"), "Review this code:")

	r, _ := resources.LoadFrom(global, "")
	prompts := r.ListPrompts()

	if len(prompts) != 1 {
		t.Fatalf("want 1 prompt, got %d", len(prompts))
	}
	if prompts[0].Name != "review" {
		t.Errorf("want name=review, got %q", prompts[0].Name)
	}
	if prompts[0].Content != "Review this code:" {
		t.Errorf("unexpected content: %q", prompts[0].Content)
	}
}

func TestLoadPrompts_LocalOverridesGlobal(t *testing.T) {
	global := t.TempDir()
	local := t.TempDir()
	writeFile(t, filepath.Join(global, "prompts", "fix.md"), "global fix")
	writeFile(t, filepath.Join(local, "prompts", "fix.md"), "local fix")

	r, _ := resources.LoadFrom(global, local)

	p, ok := r.GetPrompt("fix")
	if !ok {
		t.Fatal("expected prompt 'fix'")
	}
	if p.Content != "local fix" {
		t.Errorf("local should override global: got %q", p.Content)
	}
}

func TestLoadPrompts_NonMdFilesIgnored(t *testing.T) {
	global := t.TempDir()
	writeFile(t, filepath.Join(global, "prompts", "notes.txt"), "not a prompt")
	writeFile(t, filepath.Join(global, "prompts", "real.md"), "real prompt")

	r, _ := resources.LoadFrom(global, "")
	if len(r.ListPrompts()) != 1 {
		t.Error("only .md files should be loaded as prompts")
	}
	if _, ok := r.GetPrompt("notes"); ok {
		t.Error(".txt file should not be loaded as prompt")
	}
}

// ── extensions ─────────────────────────────────────────────────────────────────

func TestLoadExtensions_PathsDiscovered(t *testing.T) {
	global := t.TempDir()
	writeFile(t, filepath.Join(global, "extensions", "hooks.lua"), "-- lua")

	r, _ := resources.LoadFrom(global, "")
	exts := r.ListExtensionPaths()

	if len(exts) != 1 {
		t.Fatalf("want 1 extension, got %d", len(exts))
	}
	if exts[0].Name != "hooks" {
		t.Errorf("want name=hooks, got %q", exts[0].Name)
	}
}

func TestLoadExtensions_LocalOverridesGlobal(t *testing.T) {
	global := t.TempDir()
	local := t.TempDir()
	writeFile(t, filepath.Join(global, "extensions", "plugin.lua"), "-- global")
	writeFile(t, filepath.Join(local, "extensions", "plugin.lua"), "-- local")

	r, _ := resources.LoadFrom(global, local)
	ext, ok := r.GetExtensionPath("plugin")
	if !ok {
		t.Fatal("expected extension 'plugin'")
	}
	// local path should win
	if filepath.Dir(ext.Path) != filepath.Join(local, "extensions") {
		t.Errorf("local should override global, got path %q", ext.Path)
	}
}

// ── missing dirs ───────────────────────────────────────────────────────────────

func TestLoad_MissingDirsAreNoop(t *testing.T) {
	r, err := resources.LoadFrom("/nonexistent/global", "/nonexistent/local")
	if err != nil {
		t.Fatalf("LoadFrom with missing dirs should not error: %v", err)
	}
	if len(r.ListSkills()) != 0 || len(r.ListPrompts()) != 0 || len(r.ListExtensionPaths()) != 0 {
		t.Error("expected empty registry when dirs do not exist")
	}
}

// ── GetSkill/GetPrompt miss ────────────────────────────────────────────────────

func TestGetSkill_MissingReturnsFalse(t *testing.T) {
	r, _ := resources.LoadFrom("", "")
	if _, ok := r.GetSkill("nope"); ok {
		t.Error("expected false for missing skill")
	}
}

func TestGetPrompt_MissingReturnsFalse(t *testing.T) {
	r, _ := resources.LoadFrom("", "")
	if _, ok := r.GetPrompt("nope"); ok {
		t.Error("expected false for missing prompt")
	}
}

// ── GlobalConfigDir ────────────────────────────────────────────────────────────

func TestGlobalConfigDir_NotEmpty(t *testing.T) {
	dir, err := resources.GlobalConfigDir()
	if err != nil {
		t.Fatalf("GlobalConfigDir: %v", err)
	}
	if dir == "" {
		t.Error("expected non-empty dir")
	}
	if !containsStr(dir, "pigeon") {
		t.Errorf("expected 'pigeon' in path, got %q", dir)
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && findStr(s, sub))
}

func findStr(s, sub string) bool {
	for i := range s {
		if i+len(sub) <= len(s) && s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// ── Load smoke test ────────────────────────────────────────────────────────────

func TestLoad_DoesNotError(t *testing.T) {
	// Load reads real OS dirs; we just ensure it doesn't panic or error.
	reg, err := resources.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reg == nil {
		t.Error("expected non-nil registry")
	}
}

// ── ListSkills / ListPrompts / ListExtensionPaths with empty registry ──────────

func TestListSkills_Empty(t *testing.T) {
	reg, _ := resources.LoadFrom("", "")
	if skills := reg.ListSkills(); len(skills) != 0 {
		t.Errorf("expected empty, got %d", len(skills))
	}
}

func TestListPrompts_Empty(t *testing.T) {
	reg, _ := resources.LoadFrom("", "")
	if prompts := reg.ListPrompts(); len(prompts) != 0 {
		t.Errorf("expected empty, got %d", len(prompts))
	}
}

func TestListExtensionPaths_Empty(t *testing.T) {
	reg, _ := resources.LoadFrom("", "")
	if exts := reg.ListExtensionPaths(); len(exts) != 0 {
		t.Errorf("expected empty, got %d", len(exts))
	}
}

// ── GetSkill / GetPrompt miss ─────────────────────────────────────────────────

func TestGetSkill_Miss(t *testing.T) {
	reg, _ := resources.LoadFrom("", "")
	_, ok := reg.GetSkill("nonexistent")
	if ok {
		t.Error("expected miss")
	}
}

func TestGetPrompt_Miss(t *testing.T) {
	reg, _ := resources.LoadFrom("", "")
	_, ok := reg.GetPrompt("nonexistent")
	if ok {
		t.Error("expected miss")
	}
}
