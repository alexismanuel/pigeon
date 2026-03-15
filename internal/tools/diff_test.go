package tools

import (
	"strings"
	"testing"
)

func TestBuildEditDiff_changes(t *testing.T) {
	old := "line1\nline2\nline3\n"
	new := "line1\nline2 modified\nline3\n"

	res := buildEditDiff("foo.go", old, new)

	if !strings.Contains(res.summary, "foo.go") {
		t.Errorf("summary should mention filename, got: %s", res.summary)
	}
	if !strings.Contains(res.summary, "+1") {
		t.Errorf("summary should mention additions, got: %s", res.summary)
	}
	if !strings.Contains(res.summary, "-1") {
		t.Errorf("summary should mention removals, got: %s", res.summary)
	}
	if res.display == "" {
		t.Error("display should not be empty when content changed")
	}
	// Display should contain the stats header.
	if !strings.Contains(res.display, "foo.go") {
		t.Errorf("display should mention filename, got: %s", res.display)
	}
}

func TestBuildEditDiff_noChange(t *testing.T) {
	content := "same content\n"
	res := buildEditDiff("foo.go", content, content)

	if !strings.Contains(res.summary, "no changes") {
		t.Errorf("expected 'no changes' in summary, got: %s", res.summary)
	}
	if res.display != "" {
		t.Errorf("display should be empty when nothing changed, got: %s", res.display)
	}
}

func TestBuildEditDiff_multiHunk(t *testing.T) {
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = "line"
	}
	old := strings.Join(lines, "\n")
	lines[0] = "changed-top"
	lines[19] = "changed-bottom"
	new := strings.Join(lines, "\n")

	res := buildEditDiff("multi.go", old, new)

	if res.display == "" {
		t.Error("expected non-empty display for multi-hunk diff")
	}
}
