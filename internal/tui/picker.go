package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"pigeon/internal/provider/openrouter"
)

// ── messages ──────────────────────────────────────────────────────────────────

type modelPickedMsg struct{ modelID string }
type modelPickCanceledMsg struct{}
type modelLoadedMsg struct{ models []openrouter.ModelInfo }
type modelLoadErrMsg struct{ err error }

func fetchModels(catalog modelCatalog) tea.Cmd {
	return func() tea.Msg {
		models, err := catalog.ListModels(context.Background())
		if err != nil {
			return modelLoadErrMsg{err: err}
		}
		return modelLoadedMsg{models: models}
	}
}

// ── picker ────────────────────────────────────────────────────────────────────

type picker struct {
	input    textinput.Model
	all      []openrouter.ModelInfo
	filtered []openrouter.ModelInfo
	cursor   int
	offset   int
	loading  bool
	err      error
	width    int
	height   int
}

func newPicker(width, height int) picker {
	ti := textinput.New()
	ti.Placeholder = "Search models…"
	ti.Focus()
	ti.CharLimit = 0
	ti.Width = max(20, width-6)
	ti.PromptStyle = pickerPromptStyle
	ti.TextStyle = pickerInputTextStyle

	return picker{
		input:   ti,
		loading: true,
		width:   width,
		height:  height,
	}
}

func (p picker) listHeight() int {
	// 1 search line + 1 separator + 1 footer + 1 header padding = 4 overhead
	h := p.height - 4
	if h < 3 {
		return 3
	}
	return h
}

func (p picker) Update(msg tea.Msg) (picker, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.width = msg.Width
		p.height = msg.Height
		p.input.Width = max(20, msg.Width-6)
		return p, nil

	case modelLoadedMsg:
		p.loading = false
		p.err = nil
		p.all = msg.models
		p.filtered = filterModels(p.all, "")
		p.cursor = 0
		p.offset = 0
		return p, textinput.Blink

	case modelLoadErrMsg:
		p.loading = false
		p.err = msg.err
		return p, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "ctrl+c":
			return p, func() tea.Msg { return modelPickCanceledMsg{} }

		case "enter":
			if !p.loading && p.err == nil && len(p.filtered) > 0 {
				id := p.filtered[p.cursor].ID
				return p, func() tea.Msg { return modelPickedMsg{modelID: id} }
			}
			return p, nil

		case "up", "ctrl+p":
			if p.cursor > 0 {
				p.cursor--
				if p.cursor < p.offset {
					p.offset = p.cursor
				}
			}
			return p, nil

		case "down", "ctrl+n":
			if p.cursor < len(p.filtered)-1 {
				p.cursor++
				vis := p.listHeight()
				if p.cursor >= p.offset+vis {
					p.offset = p.cursor - vis + 1
				}
			}
			return p, nil
		}

		// Everything else → search input
		prev := p.input.Value()
		var cmd tea.Cmd
		p.input, cmd = p.input.Update(msg)
		if p.input.Value() != prev {
			p.filtered = filterModels(p.all, p.input.Value())
			p.cursor = 0
			p.offset = 0
		}
		return p, cmd
	}

	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	return p, cmd
}

func (p picker) View() string {
	if p.loading {
		return pickerDimStyle.Render("\n  Loading models…")
	}
	if p.err != nil {
		return lipgloss.JoinVertical(lipgloss.Left,
			pickerErrStyle.Render("  Error: "+p.err.Error()),
			pickerHintStyle.Render("  Press Esc to go back."),
		)
	}

	var b strings.Builder

	// ── search input ──────────────────────────────────────────────────────────
	b.WriteString("  ")
	b.WriteString(p.input.View())
	b.WriteString("\n")

	// ── separator ─────────────────────────────────────────────────────────────
	sep := strings.Repeat("─", max(0, p.width-4))
	b.WriteString(pickerDimStyle.Render("  "+sep) + "\n")

	// ── list ──────────────────────────────────────────────────────────────────
	vis := p.listHeight()
	end := p.offset + vis
	if end > len(p.filtered) {
		end = len(p.filtered)
	}

	if len(p.filtered) == 0 {
		b.WriteString(pickerDimStyle.Render("  no models match\n"))
	} else {
		// column widths
		nameW := 36
		idW := 42

		for i := p.offset; i < end; i++ {
			m := p.filtered[i]
			selected := i == p.cursor

			name := truncStr(m.Name, nameW)
			id := truncStr(m.ID, idW)

			ctxStr := ""
			if m.ContextLength > 0 {
				ctxStr = fmt.Sprintf("%dk ctx", m.ContextLength/1000)
			}

			providerBadge := ""
			if m.Provider != "" {
				providerBadge = fmt.Sprintf("[%s]", m.Provider)
			}
			if selected {
				cursor := pickerCursorStyle.Render("▶ ")
				row := pickerSelectedStyle.Render(
					fmt.Sprintf("%-*s  %-*s  %-11s  %-8s", nameW, name, idW, id, providerBadge, ctxStr),
				)
				b.WriteString(cursor + row + "\n")
			} else {
				cursor := "  "
				nameStr := pickerNormalStyle.Render(fmt.Sprintf("%-*s", nameW, name))
				idStr := pickerDimStyle.Render(fmt.Sprintf("  %-*s", idW, id))
				provStr := pickerProviderStyle.Render(fmt.Sprintf("  %-11s", providerBadge))
				ctxS := pickerDimStyle.Render(fmt.Sprintf("  %-8s", ctxStr))
				b.WriteString(cursor + nameStr + idStr + provStr + ctxS + "\n")
			}
		}
	}

	// ── footer ────────────────────────────────────────────────────────────────
	b.WriteString(pickerHintStyle.Render(fmt.Sprintf(
		"  ↑↓/ctrl+p/n navigate • enter select • esc cancel   %d/%d",
		len(p.filtered), len(p.all),
	)))

	return b.String()
}

// ── filter ────────────────────────────────────────────────────────────────────

func filterModels(models []openrouter.ModelInfo, query string) []openrouter.ModelInfo {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		out := make([]openrouter.ModelInfo, len(models))
		copy(out, models)
		return out
	}
	var out []openrouter.ModelInfo
	for _, m := range models {
		if strings.Contains(strings.ToLower(m.Name), q) ||
			strings.Contains(strings.ToLower(m.ID), q) ||
			strings.Contains(strings.ToLower(m.Provider), q) {
			out = append(out, m)
		}
	}
	return out
}

// ── styles ────────────────────────────────────────────────────────────────────

var (
	pickerPromptStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	pickerInputTextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	pickerSelectedStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("4"))
	pickerCursorStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	pickerNormalStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	pickerDimStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	pickerHintStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)
	pickerErrStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	pickerProviderStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("3")) // yellow for provider badge
)

// ── helpers ───────────────────────────────────────────────────────────────────

func truncStr(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-1]) + "…"
}
