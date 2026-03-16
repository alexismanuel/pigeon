package tools

import (
	"fmt"
	"strings"

	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/lipgloss"
)

var (
	gutterNumStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	gutterSepStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
)

// renderReadDisplay returns an ANSI-colourised representation of lines for the
// TUI: a right-aligned line-number gutter, a │ separator, then syntax-
// highlighted source. startLine is the 1-based number of lines[0].
func renderReadDisplay(path string, lines []string, startLine int) string {
	if len(lines) == 0 {
		return ""
	}

	// ── syntax highlight the whole block ──────────────────────────────────────
	lexer := lexers.Match(path)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	formatter := formatters.Get("terminal256")
	style := styles.Get("monokai")

	raw := strings.Join(lines, "\n")
	var highlighted strings.Builder
	iter, err := lexer.Tokenise(nil, raw)
	if err == nil {
		_ = formatter.Format(&highlighted, style, iter)
	}
	// If highlighting failed, fall back to the plain text.
	hlText := highlighted.String()
	if hlText == "" {
		hlText = raw
	}

	// Chroma's TTY formatter adds a trailing newline + reset; trim it cleanly.
	hlText = strings.TrimRight(hlText, "\n")
	hlLines := strings.Split(hlText, "\n")

	// ── gutter metrics ────────────────────────────────────────────────────────
	width := len(fmt.Sprintf("%d", startLine+len(lines)-1))
	if width < 4 {
		width = 4
	}
	sep := gutterSepStyle.Render("│")

	// ── assemble output ───────────────────────────────────────────────────────
	var b strings.Builder
	for i := range lines {
		lineNum := gutterNumStyle.Render(fmt.Sprintf("%*d", width, startLine+i))
		// Guard against chroma producing fewer lines than the input (rare).
		code := ""
		if i < len(hlLines) {
			code = hlLines[i]
		}
		b.WriteString(lineNum + sep + code + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
