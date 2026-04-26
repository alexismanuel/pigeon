package tui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"pigeon/internal/provider/openrouter"
)

// ── messages ──────────────────────────────────────────────────────────────────

type modelPickedMsg struct{ modelID string }
type modelPickCanceledMsg struct{}
type modelLoadedMsg struct{ models []openrouter.ModelInfo }
type modelLoadErrMsg struct{ err error }

// favoritesChangedMsg is emitted by the picker whenever the user toggles a
// model's favorite status. The model layer persists the new list.
type favoritesChangedMsg struct{ ids []string }

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
	// favIDs is the ordered list of favorite model IDs (persisted).
	// favModels is the resolved subset of all — populated once models load.
	favIDs    []string
	favModels []openrouter.ModelInfo
	// currentModel is the model ID currently in use (shown at top, highlighted).
	currentModel string
	// cursor is a unified index across both sections:
	//   0 .. len(favModels)-1          → favorites section
	//   len(favModels) .. total-1      → main filtered list
	cursor  int
	offset  int // scroll offset within the main list only
	loading bool
	err     error
	width   int
	height  int
}

func newPicker(width, height int, favorites []string, currentModel string) picker {
	ti := textinput.New()
	ti.Placeholder = "Search models…"
	ti.Focus()
	ti.CharLimit = 0
	ti.SetWidth(max(20, width-6))
	{
		s := ti.Styles()
		s.Focused.Prompt = pickerPromptStyle
		s.Focused.Text = pickerInputTextStyle
		ti.SetStyles(s)
	}

	favs := make([]string, len(favorites))
	copy(favs, favorites)

	return picker{
		input:        ti,
		loading:      true,
		width:        width,
		height:       height,
		favIDs:       favs,
		currentModel: currentModel,
	}
}

// ── layout helpers ────────────────────────────────────────────────────────────

// favsOverhead returns the number of lines the favorites section occupies in
// the view. Zero when there are no resolved favorites.
func (p picker) favsOverhead() int {
	if len(p.favModels) == 0 {
		return 0
	}
	// "★ Favorites" header + N items + divider line
	return 1 + len(p.favModels) + 1
}

func (p picker) listHeight() int {
	// 1 search + 1 sep + 1 footer + favorites overhead
	h := p.height - 3 - p.favsOverhead()
	if h < 3 {
		return 3
	}
	return h
}

// total returns the total number of navigable items (favorites + filtered).
func (p picker) total() int {
	return len(p.favModels) + len(p.filtered)
}

// clampCursor ensures cursor stays within [0, total).
func (p *picker) clampCursor() {
	t := p.total()
	if t == 0 {
		p.cursor = 0
		return
	}
	if p.cursor >= t {
		p.cursor = t - 1
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
}

// ── Update ────────────────────────────────────────────────────────────────────

func (p picker) Update(msg tea.Msg) (picker, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.width = msg.Width
		p.height = msg.Height
		p.input.SetWidth(max(20, msg.Width-6))
		return p, nil

	case modelLoadedMsg:
		p.loading = false
		p.err = nil
		p.all = msg.models
		p.filtered = filterModels(p.all, "")
		p.favModels = resolveFavModels(p.favIDs, p.all)
		sortCurrentModelToTop(p.filtered, p.currentModel)
		p.cursor = cursorForCurrentModel(p.filtered, p.favModels, p.currentModel)
		p.offset = 0
		return p, textinput.Blink

	case modelLoadErrMsg:
		p.loading = false
		p.err = msg.err
		return p, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc", "ctrl+c":
			return p, func() tea.Msg { return modelPickCanceledMsg{} }

		case "enter":
			if !p.loading && p.err == nil && p.total() > 0 {
				id := p.selectedID()
				return p, func() tea.Msg { return modelPickedMsg{modelID: id} }
			}
			return p, nil

		case "up", "ctrl+p":
			if p.cursor > 0 {
				p.cursor--
				p.adjustOffset()
			}
			return p, nil

		case "down", "ctrl+n":
			if p.cursor < p.total()-1 {
				p.cursor++
				p.adjustOffset()
			}
			return p, nil

		case "f":
			// Toggle the currently selected model as a favorite.
			if !p.loading && p.err == nil && p.total() > 0 {
				id := p.selectedID()
				if isFav(p.favIDs, id) {
					p.favIDs = removeFav(p.favIDs, id)
				} else {
					p.favIDs = append(p.favIDs, id)
				}
				p.favModels = resolveFavModels(p.favIDs, p.all)
				p.clampCursor()
				ids := make([]string, len(p.favIDs))
				copy(ids, p.favIDs)
				return p, func() tea.Msg { return favoritesChangedMsg{ids: ids} }
			}
			return p, nil
		}

		// Everything else → search input
		prev := p.input.Value()
		var cmd tea.Cmd
		p.input, cmd = p.input.Update(msg)
		if p.input.Value() != prev {
			p.filtered = filterModels(p.all, p.input.Value())
			sortCurrentModelToTop(p.filtered, p.currentModel)
			p.cursor = cursorForCurrentModel(p.filtered, p.favModels, p.currentModel)
			p.offset = 0
		}
		return p, cmd
	}

	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	return p, cmd
}

// selectedID returns the model ID under the cursor, or "" if nothing is
// selected (empty list).
func (p picker) selectedID() string {
	if p.cursor < len(p.favModels) {
		return p.favModels[p.cursor].ID
	}
	mainIdx := p.cursor - len(p.favModels)
	if mainIdx < len(p.filtered) {
		return p.filtered[mainIdx].ID
	}
	return ""
}

// adjustOffset keeps the main-list scroll window in sync with the cursor.
// Has no effect when the cursor is in the favorites section.
func (p *picker) adjustOffset() {
	if p.cursor < len(p.favModels) {
		return // in favorites section — no scrolling needed
	}
	mainIdx := p.cursor - len(p.favModels)
	vis := p.listHeight()
	if mainIdx < p.offset {
		p.offset = mainIdx
	}
	if mainIdx >= p.offset+vis {
		p.offset = mainIdx - vis + 1
	}
}

// ── View ──────────────────────────────────────────────────────────────────────

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

	// ── favorites section ─────────────────────────────────────────────────────
	if len(p.favModels) > 0 {
		b.WriteString(pickerFavHeaderStyle.Render("  ★ Favorites") + "\n")
		nameW, idW := 36, 42
		for i, m := range p.favModels {
			selected := i == p.cursor
			p.writeModelRow(&b, m, selected, true, nameW, idW)
		}
		b.WriteString(pickerDimStyle.Render("  "+sep) + "\n")
	}

	// ── main list ─────────────────────────────────────────────────────────────
	vis := p.listHeight()
	end := p.offset + vis
	if end > len(p.filtered) {
		end = len(p.filtered)
	}

	if len(p.filtered) == 0 {
		b.WriteString(pickerDimStyle.Render("  no models match\n"))
	} else {
		nameW, idW := 36, 42
		for i := p.offset; i < end; i++ {
			m := p.filtered[i]
			// Cursor in main list is offset by the favorites section length.
			selected := (len(p.favModels) + i) == p.cursor
			p.writeModelRow(&b, m, selected, false, nameW, idW)
		}
	}

	// ── footer ────────────────────────────────────────────────────────────────
	hint := "  ↑↓/ctrl+p/n navigate • enter select • f ★ favorite • esc cancel"
	b.WriteString(pickerHintStyle.Render(fmt.Sprintf(
		"%s   %d/%d",
		hint, len(p.filtered), len(p.all),
	)))

	return b.String()
}

// writeModelRow renders one model row into b.
// isFavSection indicates the row is being drawn inside the favorites band
// (no ★ indicator needed there since it's implied by the section header).
func (p picker) writeModelRow(b *strings.Builder, m openrouter.ModelInfo, selected, isFavSection bool, nameW, idW int) {
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

	// Star indicator shown in the main list when the model is a favorite.
	starMark := "  "
	if !isFavSection && isFav(p.favIDs, m.ID) {
		starMark = pickerFavStarStyle.Render("★ ")
	}

	// Current model indicator (shown when this model is the active one).
	currentMark := ""
	if m.ID == p.currentModel {
		currentMark = pickerCurrentStyle.Render("●")
	}

	if selected {
		cursor := pickerCursorStyle.Render("▶ ")
		label := fmt.Sprintf("%-*s  %-*s  %-11s  %-8s", nameW, name, idW, id, providerBadge, ctxStr)
		row := pickerSelectedStyle.Render(label)
		b.WriteString(cursor + row + " " + currentMark + "\n")
	} else {
		cursor := "  "
		nameStr := pickerNormalStyle.Render(fmt.Sprintf("%-*s", nameW, name))
		idStr := pickerDimStyle.Render(fmt.Sprintf("  %-*s", idW, id))
		provStr := pickerProviderStyle.Render(fmt.Sprintf("  %-11s", providerBadge))
		ctxS := pickerDimStyle.Render(fmt.Sprintf("  %-8s", ctxStr))
		b.WriteString(cursor + starMark + nameStr + idStr + provStr + ctxS + " " + currentMark + "\n")
	}
}

// ── favorites helpers ─────────────────────────────────────────────────────────

// resolveFavModels returns ModelInfo entries for each ID in ids, in order,
// skipping any IDs not found in all.
func resolveFavModels(ids []string, all []openrouter.ModelInfo) []openrouter.ModelInfo {
	byID := make(map[string]openrouter.ModelInfo, len(all))
	for _, m := range all {
		byID[m.ID] = m
	}
	out := make([]openrouter.ModelInfo, 0, len(ids))
	for _, id := range ids {
		if m, ok := byID[id]; ok {
			out = append(out, m)
		}
	}
	return out
}

func isFav(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

func removeFav(ids []string, id string) []string {
	out := ids[:0]
	for _, v := range ids {
		if v != id {
			out = append(out, v)
		}
	}
	return out
}

// ── current model helpers ──────────────────────────────────────────────────────

// sortCurrentModelToTop reorders filtered in-place so that the model with the
// given ID appears first. If the ID is empty or not found, the list is unchanged.
func sortCurrentModelToTop(filtered []openrouter.ModelInfo, currentModel string) {
	if currentModel == "" {
		return
	}
	for i, m := range filtered {
		if m.ID == currentModel {
			if i == 0 {
				return
			}
			// Shift elements down and place current model at front.
			copy(filtered[1:i+1], filtered[:i])
			filtered[0] = m
			return
		}
	}
}

// cursorForCurrentModel returns the unified cursor position that highlights the
// current model. If the current model is already a favorite, the cursor points
// to it in the favorites section. Otherwise it points to position 0 in the
// main list (where sortCurrentModelToTop placed it).
func cursorForCurrentModel(filtered []openrouter.ModelInfo, favModels []openrouter.ModelInfo, currentModel string) int {
	if currentModel == "" {
		return 0
	}
	// Check favorites section first.
	for i, m := range favModels {
		if m.ID == currentModel {
			return i
		}
	}
	// Current model should be at index 0 in the main list after sorting.
	if len(filtered) > 0 && filtered[0].ID == currentModel {
		return len(favModels)
	}
	return 0
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
	pickerProviderStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	pickerFavHeaderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true)
	pickerFavStarStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	pickerCurrentStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
)

// ── helpers ───────────────────────────────────────────────────────────────────

func truncStr(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-1]) + "…"
}
