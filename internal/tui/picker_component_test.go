package tui

// Tests for the picker component (picker.Update / picker.View).

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"pigeon/internal/provider/openrouter"
)

var pickerModels = []openrouter.ModelInfo{
	{ID: "anthropic/claude-3-5-sonnet", Name: "Claude 3.5 Sonnet", ContextLength: 200000},
	{ID: "openai/gpt-4o", Name: "GPT-4o", ContextLength: 128000},
	{ID: "openai/gpt-4o-mini", Name: "GPT-4o Mini", ContextLength: 128000},
}

func loadedPicker() picker {
	p := newPicker(120, 30)
	p2, _ := p.Update(modelLoadedMsg{models: pickerModels})
	return p2
}

// ── newPicker / listHeight ────────────────────────────────────────────────────

func TestNewPicker_StartsLoading(t *testing.T) {
	p := newPicker(80, 24)
	if !p.loading {
		t.Error("picker should start in loading state")
	}
}

func TestPickerListHeight_Positive(t *testing.T) {
	p := newPicker(80, 24)
	if p.listHeight() <= 0 {
		t.Errorf("listHeight should be positive, got %d", p.listHeight())
	}
}

func TestPickerListHeight_SmallTerminal(t *testing.T) {
	p := newPicker(80, 4)
	if p.listHeight() < 3 {
		t.Errorf("listHeight minimum should be 3, got %d", p.listHeight())
	}
}

// ── Update: loading states ────────────────────────────────────────────────────

func TestPickerUpdate_ModelLoaded(t *testing.T) {
	p := newPicker(120, 30)
	p2, _ := p.Update(modelLoadedMsg{models: pickerModels})
	if p2.loading {
		t.Error("should not be loading after modelLoadedMsg")
	}
	if len(p2.filtered) != len(pickerModels) {
		t.Errorf("expected %d models, got %d", len(pickerModels), len(p2.filtered))
	}
	if p2.cursor != 0 {
		t.Error("cursor should reset to 0 on load")
	}
}

func TestPickerUpdate_LoadError(t *testing.T) {
	p := newPicker(80, 24)
	p2, _ := p.Update(modelLoadErrMsg{err: testErr("connection refused")})
	if p2.loading {
		t.Error("should not be loading after error")
	}
	if p2.err == nil {
		t.Error("err should be set")
	}
}

// ── Update: navigation ────────────────────────────────────────────────────────

func TestPickerUpdate_DownMoveCursor(t *testing.T) {
	p := loadedPicker()
	p2, _ := p.Update(tea.KeyMsg{Type: tea.KeyDown})
	if p2.cursor != 1 {
		t.Errorf("expected cursor=1, got %d", p2.cursor)
	}
}

func TestPickerUpdate_UpAtZeroStaysZero(t *testing.T) {
	p := loadedPicker()
	p2, _ := p.Update(tea.KeyMsg{Type: tea.KeyUp})
	if p2.cursor != 0 {
		t.Errorf("up at 0 should stay 0, got %d", p2.cursor)
	}
}

func TestPickerUpdate_CtrlN_CtrlP(t *testing.T) {
	p := loadedPicker()
	p2, _ := p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n"), Alt: false})
	// ctrl+n
	p3, _ := p.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	if p3.cursor != 1 {
		t.Errorf("ctrl+n should move cursor down: got %d", p3.cursor)
	}
	_ = p2

	p4, _ := p3.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	if p4.cursor != 0 {
		t.Errorf("ctrl+p should move cursor up: got %d", p4.cursor)
	}
}

func TestPickerUpdate_DownDoesNotExceedList(t *testing.T) {
	p := loadedPicker()
	for range len(pickerModels) + 5 {
		p, _ = p.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	if p.cursor >= len(pickerModels) {
		t.Errorf("cursor exceeded list: %d >= %d", p.cursor, len(pickerModels))
	}
}

// ── Update: selection / cancel ────────────────────────────────────────────────

func TestPickerUpdate_EnterEmitsPickedMsg(t *testing.T) {
	p := loadedPicker()
	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected cmd after enter")
	}
	msg := cmd()
	picked, ok := msg.(modelPickedMsg)
	if !ok {
		t.Fatalf("expected modelPickedMsg, got %T", msg)
	}
	if picked.modelID != pickerModels[0].ID {
		t.Errorf("expected first model, got %q", picked.modelID)
	}
}

func TestPickerUpdate_EnterSelectsHighlightedModel(t *testing.T) {
	p := loadedPicker()
	p, _ = p.Update(tea.KeyMsg{Type: tea.KeyDown})
	p, _ = p.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	msg := cmd()
	picked := msg.(modelPickedMsg)
	if picked.modelID != pickerModels[2].ID {
		t.Errorf("expected third model %q, got %q", pickerModels[2].ID, picked.modelID)
	}
}

func TestPickerUpdate_EscEmitsCancelMsg(t *testing.T) {
	p := loadedPicker()
	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("expected cmd after esc")
	}
	msg := cmd()
	if _, ok := msg.(modelPickCanceledMsg); !ok {
		t.Errorf("expected modelPickCanceledMsg, got %T", msg)
	}
}

func TestPickerUpdate_CtrlC_Cancels(t *testing.T) {
	p := loadedPicker()
	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	if _, ok := cmd().(modelPickCanceledMsg); !ok {
		t.Error("ctrl+c should cancel")
	}
}

// ── Update: search input ──────────────────────────────────────────────────────

func TestPickerUpdate_TypingFilters(t *testing.T) {
	p := loadedPicker()
	for _, ch := range "claude" {
		p, _ = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
	}
	if len(p.filtered) != 1 {
		t.Errorf("expected 1 result for 'claude', got %d", len(p.filtered))
	}
	if p.filtered[0].ID != "anthropic/claude-3-5-sonnet" {
		t.Errorf("unexpected model: %q", p.filtered[0].ID)
	}
}

func TestPickerUpdate_FilterResetsCursor(t *testing.T) {
	p := loadedPicker()
	p, _ = p.Update(tea.KeyMsg{Type: tea.KeyDown})
	p, _ = p.Update(tea.KeyMsg{Type: tea.KeyDown})
	// cursor is now 2; typing should reset to 0
	p, _ = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	if p.cursor != 0 {
		t.Errorf("cursor should reset to 0 after filter change, got %d", p.cursor)
	}
}

func TestPickerUpdate_NoResultsOnEnter(t *testing.T) {
	p := loadedPicker()
	for _, ch := range "zzznomatch" {
		p, _ = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
	}
	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	// cmd should be nil (nothing to pick)
	if cmd != nil {
		t.Error("enter with no results should return nil cmd")
	}
}

// ── Update: window resize ─────────────────────────────────────────────────────

func TestPickerUpdate_WindowResize(t *testing.T) {
	p := newPicker(80, 24)
	p2, _ := p.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	if p2.width != 160 || p2.height != 50 {
		t.Errorf("expected 160x50, got %dx%d", p2.width, p2.height)
	}
}

// ── View ──────────────────────────────────────────────────────────────────────

func TestPickerView_LoadingState(t *testing.T) {
	p := newPicker(80, 24)
	view := p.View()
	if !strings.Contains(strings.ToLower(view), "loading") {
		t.Errorf("expected loading message: %q", view)
	}
}

func TestPickerView_ErrorState(t *testing.T) {
	p := newPicker(80, 24)
	p2, _ := p.Update(modelLoadErrMsg{err: testErr("timed out")})
	view := p2.View()
	if !strings.Contains(view, "timed out") {
		t.Errorf("expected error in view: %q", view)
	}
}

func TestPickerView_ShowsModels(t *testing.T) {
	p := loadedPicker()
	view := p.View()
	for _, m := range pickerModels {
		if !strings.Contains(view, m.Name) {
			t.Errorf("expected model name %q in view", m.Name)
		}
	}
}

func TestPickerView_ShowsContextLength(t *testing.T) {
	p := loadedPicker()
	view := p.View()
	if !strings.Contains(view, "200k") {
		t.Errorf("expected context length in view: %q", view)
	}
}

func TestPickerView_ShowsCursorMarker(t *testing.T) {
	p := loadedPicker()
	view := p.View()
	if !strings.Contains(view, "▶") {
		t.Errorf("expected ▶ cursor in view: %q", view)
	}
}

func TestPickerView_ShowsFooterHint(t *testing.T) {
	p := loadedPicker()
	view := p.View()
	if !strings.Contains(view, "esc") {
		t.Errorf("expected hint in footer: %q", view)
	}
}

func TestPickerView_NoModels(t *testing.T) {
	p := newPicker(80, 24)
	p2, _ := p.Update(modelLoadedMsg{models: nil})
	view := p2.View()
	if !strings.Contains(view, "no models") {
		t.Errorf("expected 'no models' message: %q", view)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

type testError string

func testErr(s string) error { return testError(s) }
func (e testError) Error() string { return string(e) }
