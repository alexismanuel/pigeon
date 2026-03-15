package tools

import (
	"strings"
	"testing"

	"github.com/aymanbagabas/go-udiff"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// forceColor makes lipgloss emit ANSI in non-TTY test environments.
func forceColor() {
	lipgloss.SetColorProfile(termenv.TrueColor)
}

// ── buildEditDiff ─────────────────────────────────────────────────────────────

func TestBuildEditDiff_noChange(t *testing.T) {
	content := "same content\n"
	res := buildEditDiff("foo.go", content, content)

	if !strings.Contains(res.summary, "no changes") {
		t.Errorf("expected 'no changes' in summary, got: %q", res.summary)
	}
	if res.display != "" {
		t.Errorf("display should be empty when nothing changed, got: %q", res.display)
	}
}

func TestBuildEditDiff_changes(t *testing.T) {
	old := "line1\nline2\nline3\n"
	new := "line1\nline2 modified\nline3\n"
	res := buildEditDiff("foo.go", old, new)

	if !strings.Contains(res.summary, "foo.go") {
		t.Errorf("summary missing filename: %q", res.summary)
	}
	if !strings.Contains(res.summary, "+1") {
		t.Errorf("summary missing addition count: %q", res.summary)
	}
	if !strings.Contains(res.summary, "-1") {
		t.Errorf("summary missing removal count: %q", res.summary)
	}
	if res.display == "" {
		t.Error("display should not be empty when content changed")
	}
	if !strings.Contains(res.display, "foo.go") {
		t.Errorf("display missing filename: %q", res.display)
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
	if strings.Count(res.display, "@@") < 2 {
		t.Errorf("expected at least 2 hunk headers, got:\n%s", res.display)
	}
}

// ── renderChangedLine ─────────────────────────────────────────────────────────

func TestRenderChangedLine_textContent(t *testing.T) {
	del := "hello world"
	ins := "hello earth"

	result := renderChangedLine(del, ins)
	parts := strings.SplitN(result, "\n", 2)
	if len(parts) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(parts), result)
	}

	delLine, insLine := parts[0], parts[1]

	// Both lines must preserve the unchanged prefix.
	if !strings.Contains(delLine, "hello ") {
		t.Errorf("del line missing unchanged prefix: %q", delLine)
	}
	if !strings.Contains(insLine, "hello ") {
		t.Errorf("ins line missing unchanged prefix: %q", insLine)
	}
	// Each line must contain the changed word.
	if !strings.Contains(delLine, "world") {
		t.Errorf("del line missing removed word: %q", delLine)
	}
	if !strings.Contains(insLine, "earth") {
		t.Errorf("ins line missing inserted word: %q", insLine)
	}
}

func TestRenderChangedLine_prefixes(t *testing.T) {
	// Strip ANSI to check the leading characters.
	result := renderChangedLine("old text", "new text")
	parts := strings.SplitN(result, "\n", 2)
	if len(parts) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(parts))
	}
	if !strings.Contains(parts[0], "-") {
		t.Errorf("del line should contain '-': %q", parts[0])
	}
	if !strings.Contains(parts[1], "+") {
		t.Errorf("ins line should contain '+': %q", parts[1])
	}
}

func TestRenderChangedLine_identical(t *testing.T) {
	// Identical lines → no crash, two lines produced.
	line := "unchanged"
	result := renderChangedLine(line, line)
	parts := strings.SplitN(result, "\n", 2)
	if len(parts) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(parts))
	}
}

// ── renderLineWithHighlights — highlight colours (requires forced color) ──────

// ansiDelHigh and ansiInsHigh are the 24-bit background ANSI sequences that
// lipgloss emits for the highlight styles (#7a2f2f and #2f7a30 respectively).
// termenv converts hex → RGB with a floor divide so 0x7a (122) renders as 121.
const (
	ansiDelHigh = "48;2;121;47;47" // diffDelHighStyle background
	ansiInsHigh = "48;2;47;121;48" // diffAddHighStyle background
)

func TestRenderLineWithHighlights_highlightsApplied(t *testing.T) {
	forceColor()

	del := "hello world"
	ins := "hello earth"
	charEdits := udiff.Strings(del, ins)

	delLine := renderLineWithHighlights(del, charEdits, false)
	insLine := renderLineWithHighlights(ins, charEdits, true)

	// The bright highlight background must be emitted for the changed chars.
	if !strings.Contains(delLine, ansiDelHigh) {
		t.Errorf("del highlight background missing in: %q", delLine)
	}
	if !strings.Contains(insLine, ansiInsHigh) {
		t.Errorf("ins highlight background missing in: %q", insLine)
	}
}

func TestRenderLineWithHighlights_unchangedNoHighlight(t *testing.T) {
	forceColor()

	line := "same line"
	charEdits := udiff.Strings(line, line) // empty — no changes

	delLine := renderLineWithHighlights(line, charEdits, false)
	insLine := renderLineWithHighlights(line, charEdits, true)

	// No changed characters → highlight colours must NOT appear.
	if strings.Contains(delLine, ansiDelHigh) {
		t.Errorf("unexpected del highlight for identical line: %q", delLine)
	}
	if strings.Contains(insLine, ansiInsHigh) {
		t.Errorf("unexpected ins highlight for identical line: %q", insLine)
	}
}

func TestRenderLineWithHighlights_pureInsertion(t *testing.T) {
	forceColor()

	del := "foo"
	ins := "foo bar"
	charEdits := udiff.Strings(del, ins)

	insLine := renderLineWithHighlights(ins, charEdits, true)

	if !strings.Contains(insLine, "bar") {
		t.Errorf("ins line missing inserted text: %q", insLine)
	}
	if !strings.Contains(insLine, ansiInsHigh) {
		t.Errorf("ins highlight background missing: %q", insLine)
	}
}

func TestRenderLineWithHighlights_pureDeletion(t *testing.T) {
	forceColor()

	del := "foo bar"
	ins := "foo"
	charEdits := udiff.Strings(del, ins)

	delLine := renderLineWithHighlights(del, charEdits, false)

	if !strings.Contains(delLine, "bar") {
		t.Errorf("del line missing deleted text: %q", delLine)
	}
	if !strings.Contains(delLine, ansiDelHigh) {
		t.Errorf("del highlight background missing: %q", delLine)
	}
}
