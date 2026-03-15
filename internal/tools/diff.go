package tools

import (
	"fmt"
	"strings"

	"github.com/aymanbagabas/go-udiff"
	"github.com/charmbracelet/lipgloss"
)

// ── styles ────────────────────────────────────────────────────────────────────

var (
	// Line-level backgrounds for deleted / inserted / context lines.
	diffDelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Background(lipgloss.Color("#33201e"))
	diffAddStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Background(lipgloss.Color("#1e3320"))
	diffCtxStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	diffHunkStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	diffFileStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true)

	// Intra-line highlights: brighter background for the exact chars that changed.
	diffDelHighStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("#7a2f2f")).Bold(true)
	diffAddHighStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("#2f7a30")).Bold(true)

	// Stats header.
	diffStatFile = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	diffStatAdd  = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
	diffStatDel  = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
)

// ── public API ────────────────────────────────────────────────────────────────

// editDiffResult holds both the AI-visible summary and the TUI display string.
type editDiffResult struct {
	// summary is sent back to the model — short, no ANSI.
	summary string
	// display is shown in the TUI — colorised unified diff with intra-line
	// character highlighting.
	display string
}

// buildEditDiff computes the diff between oldContent and newContent.
func buildEditDiff(path, oldContent, newContent string) editDiffResult {
	edits := udiff.Lines(oldContent, newContent)
	unified, err := udiff.ToUnifiedDiff("a/"+path, "b/"+path, oldContent, edits, 3)
	if err != nil {
		return editDiffResult{summary: fmt.Sprintf("Edited %s", path)}
	}
	if len(unified.Hunks) == 0 {
		return editDiffResult{summary: fmt.Sprintf("Edited %s (no changes)", path)}
	}

	additions, removals := countStats(unified)
	summary := fmt.Sprintf("Edited %s (+%d -%d)", path, additions, removals)
	display := renderColorizedDiff(path, unified, additions, removals)
	return editDiffResult{summary: summary, display: display}
}

// ── stats ─────────────────────────────────────────────────────────────────────

func countStats(unified udiff.UnifiedDiff) (additions, removals int) {
	for _, hunk := range unified.Hunks {
		for _, line := range hunk.Lines {
			switch line.Kind {
			case udiff.Insert:
				additions++
			case udiff.Delete:
				removals++
			}
		}
	}
	return
}

// ── renderer ─────────────────────────────────────────────────────────────────

func renderColorizedDiff(path string, unified udiff.UnifiedDiff, additions, removals int) string {
	var b strings.Builder

	// Stats header line.
	b.WriteString(diffStatFile.Render(path + "  "))
	b.WriteString(diffStatAdd.Render(fmt.Sprintf("+%d", additions)))
	b.WriteString(diffStatFile.Render("  "))
	b.WriteString(diffStatDel.Render(fmt.Sprintf("-%d", removals)))
	b.WriteRune('\n')

	for _, hunk := range unified.Hunks {
		// Hunk header.
		header := fmt.Sprintf("@@ -%d +%d @@", hunk.FromLine, hunk.ToLine)
		b.WriteString(diffHunkStyle.Render(header))
		b.WriteRune('\n')

		renderHunk(&b, hunk)
	}

	return strings.TrimRight(b.String(), "\n")
}

// renderHunk groups consecutive delete/insert runs, pairs them up, and renders
// each pair with intra-line character-level highlighting.
func renderHunk(b *strings.Builder, hunk *udiff.Hunk) {
	var pendingDels, pendingIns []string

	flush := func() {
		if len(pendingDels) == 0 && len(pendingIns) == 0 {
			return
		}
		n := max(len(pendingDels), len(pendingIns))
		for i := 0; i < n; i++ {
			switch {
			case i < len(pendingDels) && i < len(pendingIns):
				// Paired del+ins — render with intra-line char highlighting.
				del, ins := pendingDels[i], pendingIns[i]
				b.WriteString(renderChangedLine(del, ins))
			case i < len(pendingDels):
				b.WriteString(diffDelStyle.Render("- " + pendingDels[i]))
			default:
				b.WriteString(diffAddStyle.Render("+ " + pendingIns[i]))
			}
			b.WriteRune('\n')
		}
		pendingDels = pendingDels[:0]
		pendingIns = pendingIns[:0]
	}

	for _, l := range hunk.Lines {
		content := strings.TrimSuffix(l.Content, "\n")
		switch l.Kind {
		case udiff.Delete:
			// If inserts already queued (unusual insert-before-delete order), flush first.
			if len(pendingIns) > 0 {
				flush()
			}
			pendingDels = append(pendingDels, content)
		case udiff.Insert:
			pendingIns = append(pendingIns, content)
		case udiff.Equal:
			flush()
			b.WriteString(diffCtxStyle.Render("  " + content))
			b.WriteRune('\n')
		}
	}
	flush()
}

// renderChangedLine renders a paired (deleted, inserted) line with intra-line
// character-level highlighting on the changed characters.
func renderChangedLine(del, ins string) string {
	// Compute character-level edits between the two lines.
	charEdits := udiff.Strings(del, ins)

	delRendered := renderLineWithHighlights(del, charEdits, false)
	insRendered := renderLineWithHighlights(ins, charEdits, true)
	return delRendered + "\n" + insRendered
}

// renderLineWithHighlights renders one line (del or ins) with the characters
// that were changed highlighted with a brighter background.
//
// charEdits are the byte-level edits from udiff.Strings(del, ins).
// isIns=false → render the "before" (delete) line; isIns=true → the "after" (insert) line.
func renderLineWithHighlights(line string, charEdits []udiff.Edit, isIns bool) string {
	baseStyle := diffDelStyle
	highStyle := diffDelHighStyle
	prefix := "- "
	if isIns {
		baseStyle = diffAddStyle
		highStyle = diffAddHighStyle
		prefix = "+ "
	}

	// No char-level data → whole line in base style.
	if len(charEdits) == 0 {
		return baseStyle.Render(prefix + line)
	}

	type segment struct {
		text string
		hi   bool
	}
	var segs []segment

	if isIns {
		// Walk the INSERT (after) line.
		// Equal regions come from the matching bytes of the before line;
		// highlighted regions are the edit.New spans.
		curBefore, curAfter := 0, 0
		for _, e := range charEdits {
			equalLen := e.Start - curBefore
			if equalLen > 0 {
				segs = append(segs, segment{line[curAfter : curAfter+equalLen], false})
				curAfter += equalLen
			}
			curBefore = e.End
			if len(e.New) > 0 {
				segs = append(segs, segment{e.New, true})
				curAfter += len(e.New)
			}
		}
		// Trailing equal section.
		if curAfter < len(line) {
			segs = append(segs, segment{line[curAfter:], false})
		}
	} else {
		// Walk the DELETE (before) line.
		// Highlighted regions are the spans replaced/deleted by each edit.
		cur := 0
		for _, e := range charEdits {
			if e.Start > cur {
				segs = append(segs, segment{line[cur:e.Start], false})
			}
			if e.End > e.Start {
				segs = append(segs, segment{line[e.Start:e.End], true})
			}
			cur = e.End
		}
		if cur < len(line) {
			segs = append(segs, segment{line[cur:], false})
		}
	}

	var b strings.Builder
	b.WriteString(baseStyle.Render(prefix))
	for _, s := range segs {
		if s.hi {
			b.WriteString(highStyle.Render(s.text))
		} else {
			b.WriteString(baseStyle.Render(s.text))
		}
	}
	return b.String()
}
