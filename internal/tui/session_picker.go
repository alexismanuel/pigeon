package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── messages ──────────────────────────────────────────────────────────────────

type sessionPickedMsg struct{ sessionID string }
type sessionPickCanceledMsg struct{}
type sessionsLoadedMsg struct{ rows []sessionRow }
type sessionsLoadErrMsg struct{ err error }

type sessionRow struct {
	ID          string
	Label       string
	FirstPrompt string
	UpdatedAt   time.Time
}

func fetchSessions(store sessionStore) tea.Cmd {
	return func() tea.Msg {
		metas, err := store.ListSessions(50)
		if err != nil {
			return sessionsLoadErrMsg{err: err}
		}
		rows := make([]sessionRow, 0, len(metas))
		for _, meta := range metas {
			label, _ := store.GetSessionLabel(meta.ID)
			first, _ := store.GetFirstUserMessage(meta.ID)
			rows = append(rows, sessionRow{
				ID:          meta.ID,
				Label:       label,
				FirstPrompt: first,
				UpdatedAt:   meta.UpdatedAt,
			})
		}
		return sessionsLoadedMsg{rows: rows}
	}
}

// ── sessionPicker ─────────────────────────────────────────────────────────────

type sessionPicker struct {
	input    textinput.Model
	all      []sessionRow
	filtered []sessionRow
	cursor   int
	offset   int
	loading  bool
	err      error
	width    int
	height   int
}

func newSessionPicker(width, height int) sessionPicker {
	ti := textinput.New()
	ti.Placeholder = "Filter sessions…"
	ti.Focus()
	ti.CharLimit = 0
	ti.Width = max(20, width-6)
	ti.PromptStyle = pickerPromptStyle
	ti.TextStyle = pickerInputTextStyle

	return sessionPicker{
		input:   ti,
		loading: true,
		width:   width,
		height:  height,
	}
}

func (p sessionPicker) listHeight() int {
	h := p.height - 4 // search + separator + footer + padding
	if h < 3 {
		return 3
	}
	return h
}

func (p sessionPicker) Update(msg tea.Msg) (sessionPicker, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.width = msg.Width
		p.height = msg.Height
		p.input.Width = max(20, msg.Width-6)
		return p, nil

	case sessionsLoadedMsg:
		p.loading = false
		p.err = nil
		p.all = msg.rows
		p.filtered = filterSessions(p.all, "")
		p.cursor = 0
		p.offset = 0
		return p, textinput.Blink

	case sessionsLoadErrMsg:
		p.loading = false
		p.err = msg.err
		return p, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "ctrl+c":
			return p, func() tea.Msg { return sessionPickCanceledMsg{} }

		case "enter":
			if !p.loading && p.err == nil && len(p.filtered) > 0 {
				id := p.filtered[p.cursor].ID
				return p, func() tea.Msg { return sessionPickedMsg{sessionID: id} }
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

		prev := p.input.Value()
		var cmd tea.Cmd
		p.input, cmd = p.input.Update(msg)
		if p.input.Value() != prev {
			p.filtered = filterSessions(p.all, p.input.Value())
			p.cursor = 0
			p.offset = 0
		}
		return p, cmd
	}

	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	return p, cmd
}

func (p sessionPicker) View() string {
	if p.loading {
		return pickerDimStyle.Render("\n  Loading sessions…")
	}
	if p.err != nil {
		return lipgloss.JoinVertical(lipgloss.Left,
			pickerErrStyle.Render("  Error: "+p.err.Error()),
			pickerHintStyle.Render("  Press Esc to go back."),
		)
	}

	var b strings.Builder

	// search input
	b.WriteString("  ")
	b.WriteString(p.input.View())
	b.WriteString("\n")

	// separator
	sep := strings.Repeat("─", max(0, p.width-4))
	b.WriteString(pickerDimStyle.Render("  "+sep) + "\n")

	// list
	vis := p.listHeight()
	end := p.offset + vis
	if end > len(p.filtered) {
		end = len(p.filtered)
	}

	const (
		idW      = 8
		previewW = 40
		dateW    = 10
	)

	if len(p.filtered) == 0 {
		b.WriteString(pickerDimStyle.Render("  no sessions match\n"))
	} else {
		for i := p.offset; i < end; i++ {
			row := p.filtered[i]
			selected := i == p.cursor

			shortID := row.ID
			if len(shortID) > idW {
				shortID = shortID[:idW]
			}
			preview := buildPreview(row.Label, row.FirstPrompt, previewW)
			date := relTime(row.UpdatedAt)

			if selected {
				cursor := pickerCursorStyle.Render("▶ ")
				line := pickerSelectedStyle.Render(
					fmt.Sprintf("%-*s  %-*s  %-*s", idW, shortID, previewW, preview, dateW, date),
				)
				b.WriteString(cursor + line + "\n")
			} else {
				cursor := "  "
				idStr := pickerNormalStyle.Render(fmt.Sprintf("%-*s", idW, shortID))
				previewStr := pickerDimStyle.Render(fmt.Sprintf("  %-*s", previewW, preview))
				dateStr := pickerDimStyle.Render(fmt.Sprintf("  %-*s", dateW, date))
				b.WriteString(cursor + idStr + previewStr + dateStr + "\n")
			}
		}
	}

	// footer
	b.WriteString(pickerHintStyle.Render(fmt.Sprintf(
		"  ↑↓/ctrl+p/n navigate • enter select • esc cancel   %d/%d",
		len(p.filtered), len(p.all),
	)))

	return b.String()
}

// ── filter ────────────────────────────────────────────────────────────────────

func filterSessions(rows []sessionRow, query string) []sessionRow {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		out := make([]sessionRow, len(rows))
		copy(out, rows)
		return out
	}
	var out []sessionRow
	for _, r := range rows {
		if strings.Contains(strings.ToLower(r.ID), q) ||
			strings.Contains(strings.ToLower(r.Label), q) ||
			strings.Contains(strings.ToLower(r.FirstPrompt), q) {
			out = append(out, r)
		}
	}
	return out
}

// buildPreview returns the display string for the preview column.
// If a label exists it's shown as "[label] " followed by the first-prompt
// remainder; otherwise just the first prompt. Total width is capped at maxW.
func buildPreview(label, firstPrompt string, maxW int) string {
	if label != "" {
		return truncStr("["+label+"] "+firstPrompt, maxW)
	}
	if firstPrompt == "" {
		return "—"
	}
	return truncStr(firstPrompt, maxW)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func relTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("Jan 2")
	}
}
