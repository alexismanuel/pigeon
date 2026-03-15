package tools

import (
	"fmt"
	"strings"

	"github.com/aymanbagabas/go-udiff"
	"github.com/charmbracelet/lipgloss"
)

// Diff line styles — mirrors the dark-mode palette used by crush.
var (
	diffAddStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Background(lipgloss.Color("#1e3320"))
	diffDelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Background(lipgloss.Color("#33201e"))
	diffHunkStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	diffFileStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true)
	diffCtxStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	diffStatAdd   = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
	diffStatDel   = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	diffStatFile  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

// editDiffResult holds both the AI-visible summary and the TUI display string.
type editDiffResult struct {
	// summary is sent back to the model — short, no ANSI.
	summary string
	// display is shown in the TUI — colorised unified diff.
	display string
}

// buildEditDiff computes the diff between oldContent and newContent, returns a
// short summary string for the AI and a colorised diff for the TUI.
func buildEditDiff(path, oldContent, newContent string) editDiffResult {
	unified := udiff.Unified("a/"+path, "b/"+path, oldContent, newContent)

	if strings.TrimSpace(unified) == "" {
		return editDiffResult{summary: fmt.Sprintf("Edited %s (no changes)", path)}
	}

	// Count additions / removals for the summary line.
	additions, removals := 0, 0
	for _, line := range strings.Split(unified, "\n") {
		switch {
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			additions++
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			removals++
		}
	}

	summary := fmt.Sprintf("Edited %s (+%d -%d)", path, additions, removals)
	display := renderDiff(path, unified, additions, removals)

	return editDiffResult{summary: summary, display: display}
}

// renderDiff turns a raw unified-diff string into a colourised TUI string.
func renderDiff(path, unified string, additions, removals int) string {
	lines := strings.Split(unified, "\n")
	// Drop trailing blank line that udiff always appends.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	var b strings.Builder

	// ── stats header ─────────────────────────────────────────────────────────
	b.WriteString(diffStatFile.Render(path+"  "))
	b.WriteString(diffStatAdd.Render(fmt.Sprintf("+%d", additions)))
	b.WriteString(diffStatFile.Render("  "))
	b.WriteString(diffStatDel.Render(fmt.Sprintf("-%d", removals)))
	b.WriteRune('\n')

	// ── diff lines ────────────────────────────────────────────────────────────
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ "):
			b.WriteString(diffFileStyle.Render(line))
		case strings.HasPrefix(line, "@@"):
			b.WriteString(diffHunkStyle.Render(line))
		case strings.HasPrefix(line, "+"):
			b.WriteString(diffAddStyle.Render(line))
		case strings.HasPrefix(line, "-"):
			b.WriteString(diffDelStyle.Render(line))
		default:
			b.WriteString(diffCtxStyle.Render(line))
		}
		b.WriteRune('\n')
	}

	return strings.TrimRight(b.String(), "\n")
}
