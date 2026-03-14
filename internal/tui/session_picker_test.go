package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"pigeon/internal/provider/openrouter"
)

var now = time.Now()

var testSessionRows = []sessionRow{
	{ID: "abc12345deadbeef", Label: "", FirstPrompt: "Explain how goroutines work", UpdatedAt: now.Add(-2 * time.Hour)},
	{ID: "def67890cafebabe", Label: "refactor project", FirstPrompt: "Refactor the auth module", UpdatedAt: now.Add(-24 * time.Hour)},
	{ID: "fff00000aaaabbbb", Label: "", FirstPrompt: "Write unit tests for the parser", UpdatedAt: now.Add(-72 * time.Hour)},
}

func loadedSessionPicker() sessionPicker {
	p := newSessionPicker(120, 30)
	p2, _ := p.Update(sessionsLoadedMsg{rows: testSessionRows})
	return p2
}

// ── newSessionPicker / listHeight ─────────────────────────────────────────────

func TestNewSessionPicker_StartsLoading(t *testing.T) {
	p := newSessionPicker(80, 24)
	if !p.loading {
		t.Error("picker should start in loading state")
	}
}

func TestSessionPickerListHeight_Positive(t *testing.T) {
	p := newSessionPicker(80, 24)
	if p.listHeight() <= 0 {
		t.Errorf("listHeight should be positive, got %d", p.listHeight())
	}
}

// ── Update: loading states ────────────────────────────────────────────────────

func TestSessionPickerUpdate_Loaded(t *testing.T) {
	p := newSessionPicker(120, 30)
	p2, _ := p.Update(sessionsLoadedMsg{rows: testSessionRows})
	if p2.loading {
		t.Error("should not be loading after sessionsLoadedMsg")
	}
	if len(p2.filtered) != len(testSessionRows) {
		t.Errorf("expected %d rows, got %d", len(testSessionRows), len(p2.filtered))
	}
	if p2.cursor != 0 {
		t.Error("cursor should reset to 0")
	}
}

func TestSessionPickerUpdate_LoadError(t *testing.T) {
	p := newSessionPicker(80, 24)
	p2, _ := p.Update(sessionsLoadErrMsg{err: testErr("db error")})
	if p2.loading {
		t.Error("should not be loading after error")
	}
	if p2.err == nil {
		t.Error("err should be set")
	}
}

// ── Update: navigation ────────────────────────────────────────────────────────

func TestSessionPickerUpdate_Down(t *testing.T) {
	p := loadedSessionPicker()
	p2, _ := p.Update(tea.KeyMsg{Type: tea.KeyDown})
	if p2.cursor != 1 {
		t.Errorf("expected cursor=1, got %d", p2.cursor)
	}
}

func TestSessionPickerUpdate_UpAtZeroStays(t *testing.T) {
	p := loadedSessionPicker()
	p2, _ := p.Update(tea.KeyMsg{Type: tea.KeyUp})
	if p2.cursor != 0 {
		t.Errorf("up at 0 should stay 0, got %d", p2.cursor)
	}
}

func TestSessionPickerUpdate_CtrlN_CtrlP(t *testing.T) {
	p := loadedSessionPicker()
	p2, _ := p.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	if p2.cursor != 1 {
		t.Errorf("ctrl+n should move cursor down: got %d", p2.cursor)
	}
	p3, _ := p2.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	if p3.cursor != 0 {
		t.Errorf("ctrl+p should move cursor up: got %d", p3.cursor)
	}
}

func TestSessionPickerUpdate_DownCap(t *testing.T) {
	p := loadedSessionPicker()
	for range len(testSessionRows) + 5 {
		p, _ = p.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	if p.cursor >= len(testSessionRows) {
		t.Errorf("cursor exceeded list: %d >= %d", p.cursor, len(testSessionRows))
	}
}

// ── Update: selection / cancel ────────────────────────────────────────────────

func TestSessionPickerUpdate_EnterEmitsPickedMsg(t *testing.T) {
	p := loadedSessionPicker()
	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected cmd after enter")
	}
	msg := cmd()
	picked, ok := msg.(sessionPickedMsg)
	if !ok {
		t.Fatalf("expected sessionPickedMsg, got %T", msg)
	}
	if picked.sessionID != testSessionRows[0].ID {
		t.Errorf("expected first session, got %q", picked.sessionID)
	}
}

func TestSessionPickerUpdate_EnterSelectsHighlighted(t *testing.T) {
	p := loadedSessionPicker()
	p, _ = p.Update(tea.KeyMsg{Type: tea.KeyDown})
	p, _ = p.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	msg := cmd()
	picked := msg.(sessionPickedMsg)
	if picked.sessionID != testSessionRows[2].ID {
		t.Errorf("expected third session %q, got %q", testSessionRows[2].ID, picked.sessionID)
	}
}

func TestSessionPickerUpdate_EscCancels(t *testing.T) {
	p := loadedSessionPicker()
	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("expected cmd after esc")
	}
	if _, ok := cmd().(sessionPickCanceledMsg); !ok {
		t.Error("expected sessionPickCanceledMsg")
	}
}

func TestSessionPickerUpdate_CtrlC_Cancels(t *testing.T) {
	p := loadedSessionPicker()
	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	if _, ok := cmd().(sessionPickCanceledMsg); !ok {
		t.Error("ctrl+c should cancel")
	}
}

func TestSessionPickerUpdate_NoResultsEnterNoOp(t *testing.T) {
	p := loadedSessionPicker()
	for _, ch := range "zzznomatch" {
		p, _ = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
	}
	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Error("enter with no results should return nil cmd")
	}
}

// ── Update: search ────────────────────────────────────────────────────────────

func TestSessionPickerUpdate_FilterByLabel(t *testing.T) {
	p := loadedSessionPicker()
	for _, ch := range "refactor" {
		p, _ = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
	}
	if len(p.filtered) != 1 {
		t.Errorf("expected 1 result for 'refactor', got %d", len(p.filtered))
	}
}

func TestSessionPickerUpdate_FilterByID(t *testing.T) {
	p := loadedSessionPicker()
	for _, ch := range "abc1" {
		p, _ = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
	}
	if len(p.filtered) != 1 {
		t.Errorf("expected 1 result for 'abc1', got %d", len(p.filtered))
	}
}

func TestSessionPickerUpdate_FilterResetsCursor(t *testing.T) {
	p := loadedSessionPicker()
	p, _ = p.Update(tea.KeyMsg{Type: tea.KeyDown})
	p, _ = p.Update(tea.KeyMsg{Type: tea.KeyDown})
	p, _ = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if p.cursor != 0 {
		t.Errorf("cursor should reset to 0 after filter, got %d", p.cursor)
	}
}

// ── Update: window resize ─────────────────────────────────────────────────────

func TestSessionPickerUpdate_Resize(t *testing.T) {
	p := newSessionPicker(80, 24)
	p2, _ := p.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	if p2.width != 160 || p2.height != 50 {
		t.Errorf("expected 160x50, got %dx%d", p2.width, p2.height)
	}
}

// ── View ──────────────────────────────────────────────────────────────────────

func TestSessionPickerView_Loading(t *testing.T) {
	p := newSessionPicker(80, 24)
	if !strings.Contains(strings.ToLower(p.View()), "loading") {
		t.Error("expected loading message")
	}
}

func TestSessionPickerView_Error(t *testing.T) {
	p := newSessionPicker(80, 24)
	p2, _ := p.Update(sessionsLoadErrMsg{err: testErr("timeout")})
	if !strings.Contains(p2.View(), "timeout") {
		t.Errorf("expected error in view: %q", p2.View())
	}
}

func TestSessionPickerView_ShowsRows(t *testing.T) {
	p := loadedSessionPicker()
	view := p.View()
	// Short IDs should appear.
	if !strings.Contains(view, "abc12345") {
		t.Errorf("expected short ID in view: %q", view)
	}
	// First prompt should appear.
	if !strings.Contains(view, "goroutines") {
		t.Errorf("expected first prompt in view: %q", view)
	}
	// Label should appear with brackets.
	if !strings.Contains(view, "[refactor project]") {
		t.Errorf("expected label in view: %q", view)
	}
}

func TestSessionPickerView_ShowsCursor(t *testing.T) {
	p := loadedSessionPicker()
	if !strings.Contains(p.View(), "▶") {
		t.Errorf("expected cursor marker ▶")
	}
}

func TestSessionPickerView_NoRows(t *testing.T) {
	p := newSessionPicker(80, 24)
	p2, _ := p.Update(sessionsLoadedMsg{rows: nil})
	if !strings.Contains(p2.View(), "no sessions match") {
		t.Errorf("expected 'no sessions match': %q", p2.View())
	}
}

func TestSessionPickerView_Footer(t *testing.T) {
	p := loadedSessionPicker()
	if !strings.Contains(p.View(), "esc") {
		t.Errorf("expected footer hint: %q", p.View())
	}
}

// ── filterSessions ────────────────────────────────────────────────────────────

func TestFilterSessions_Empty(t *testing.T) {
	got := filterSessions(testSessionRows, "")
	if len(got) != len(testSessionRows) {
		t.Errorf("empty query should return all, got %d", len(got))
	}
}

func TestFilterSessions_ByLabel(t *testing.T) {
	got := filterSessions(testSessionRows, "refactor")
	if len(got) != 1 || got[0].Label != "refactor project" {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestFilterSessions_ByFirstPrompt(t *testing.T) {
	got := filterSessions(testSessionRows, "goroutines")
	if len(got) != 1 || !strings.Contains(got[0].FirstPrompt, "goroutines") {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestFilterSessions_ByID(t *testing.T) {
	got := filterSessions(testSessionRows, "fff0")
	if len(got) != 1 {
		t.Errorf("expected 1 result, got %d", len(got))
	}
}

func TestFilterSessions_NoMatch(t *testing.T) {
	if got := filterSessions(testSessionRows, "zzznomatch"); len(got) != 0 {
		t.Errorf("expected 0 results, got %d", len(got))
	}
}

// ── relTime ───────────────────────────────────────────────────────────────────

func TestRelTime_JustNow(t *testing.T) {
	if got := relTime(time.Now().Add(-5 * time.Second)); got != "just now" {
		t.Errorf("expected 'just now', got %q", got)
	}
}

func TestRelTime_Minutes(t *testing.T) {
	got := relTime(time.Now().Add(-30 * time.Minute))
	if !strings.Contains(got, "m ago") {
		t.Errorf("expected minutes format, got %q", got)
	}
}

func TestRelTime_Hours(t *testing.T) {
	got := relTime(time.Now().Add(-3 * time.Hour))
	if !strings.Contains(got, "h ago") {
		t.Errorf("expected hours format, got %q", got)
	}
}

func TestRelTime_Days(t *testing.T) {
	got := relTime(time.Now().Add(-48 * time.Hour))
	if !strings.Contains(got, "d ago") {
		t.Errorf("expected days format, got %q", got)
	}
}

func TestRelTime_OldDate(t *testing.T) {
	got := relTime(time.Now().Add(-30 * 24 * time.Hour))
	// should be "Jan 2" style, not "X ago"
	if strings.Contains(got, "ago") {
		t.Errorf("old date should use calendar format, got %q", got)
	}
}

// ── TUI model: resumeMode ─────────────────────────────────────────────────────

func TestUpdateResumePicker_Cancelled(t *testing.T) {
	m := newTestModel()
	m.mode = resumeMode
	m.sessionPicker = newSessionPicker(80, 24)

	next, _ := m.Update(sessionPickCanceledMsg{})
	tm := next.(Model)
	if tm.mode != chatMode {
		t.Error("expected chat mode after cancel")
	}
}

func TestUpdateResumePicker_LoadedMsg(t *testing.T) {
	m := newTestModel()
	m.mode = resumeMode
	m.sessionPicker = newSessionPicker(80, 24)

	next, _ := m.Update(sessionsLoadedMsg{rows: testSessionRows})
	tm := next.(Model)
	if tm.sessionPicker.loading {
		t.Error("session picker should not be loading after rows received")
	}
	if len(tm.sessionPicker.filtered) != len(testSessionRows) {
		t.Errorf("expected %d rows", len(testSessionRows))
	}
}

func TestUpdateResumePicker_Picked_WithStore(t *testing.T) {
	m, mgr, id := newModelWithSessions(t)
	mgr.AppendMessages(id, "", []openrouter.Message{{Role: "user", Content: "hi"}})

	m.mode = resumeMode
	m.sessionPicker = newSessionPicker(80, 24)

	next, _ := m.Update(sessionPickedMsg{sessionID: id})
	tm := next.(Model)
	if tm.mode != chatMode {
		t.Error("expected chat mode after pick")
	}
	if tm.sessionID != id {
		t.Errorf("expected session %q, got %q", id, tm.sessionID)
	}
	if len(tm.history) == 0 {
		t.Error("expected history loaded after pick")
	}
}

func TestHandleCommand_Sessions_OpensPickerMode(t *testing.T) {
	m, _, _ := newModelWithSessions(t)
	next, cmd := m.handleCommand("/sessions")
	tm := next.(Model)
	if tm.mode != resumeMode {
		t.Errorf("expected resumeMode, got %d", tm.mode)
	}
	if cmd == nil {
		t.Error("expected fetchSessions cmd")
	}
}

func TestView_ResumeModeShowsPicker(t *testing.T) {
	m := newTestModel()
	m.mode = resumeMode
	m.sessionPicker = newSessionPicker(80, 24)
	view := m.View()
	if !strings.Contains(strings.ToLower(view), "loading") {
		t.Errorf("expected session picker in view: %q", view)
	}
}

// ── buildPreview ──────────────────────────────────────────────────────────────

func TestBuildPreview_NoLabel(t *testing.T) {
	got := buildPreview("", "explain goroutines please", 40)
	if got != "explain goroutines please" {
		t.Errorf("unexpected: %q", got)
	}
}

func TestBuildPreview_WithLabel(t *testing.T) {
	got := buildPreview("refactor", "rewrite the auth module", 40)
	if !strings.HasPrefix(got, "[refactor] ") {
		t.Errorf("expected label prefix, got %q", got)
	}
}

func TestBuildPreview_TruncatesLong(t *testing.T) {
	long := strings.Repeat("x", 100)
	got := buildPreview("", long, 40)
	runes := []rune(got)
	if len(runes) > 40 {
		t.Errorf("expected max 40 runes, got %d", len(runes))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected ellipsis, got %q", got)
	}
}

func TestBuildPreview_EmptyBoth(t *testing.T) {
	got := buildPreview("", "", 40)
	if got != "—" {
		t.Errorf("expected em dash for empty, got %q", got)
	}
}

func TestBuildPreview_LabelFitsWithinWidth(t *testing.T) {
	label := "my work"
	prompt := strings.Repeat("a", 50)
	got := buildPreview(label, prompt, 40)
	if len([]rune(got)) > 40 {
		t.Errorf("result exceeds maxW: %q", got)
	}
}

// ── /label command ────────────────────────────────────────────────────────────

func TestHandleCommand_Label_Set(t *testing.T) {
	m, _, id := newModelWithSessions(t)
	_ = id
	next, _ := m.handleCommand("/label my great session")
	tm := next.(Model)
	found := false
	for _, l := range tm.lines {
		if strings.Contains(l, "my great session") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected confirmation line: %v", tm.lines)
	}
	// Verify it was persisted.
	label, err := m.sessions.GetSessionLabel(m.sessionID)
	if err != nil {
		t.Fatalf("GetSessionLabel: %v", err)
	}
	if label != "my great session" {
		t.Errorf("expected label to be persisted, got %q", label)
	}
}

func TestHandleCommand_Label_ShowCurrent(t *testing.T) {
	m, _, _ := newModelWithSessions(t)
	m.sessions.SetSessionLabel(m.sessionID, "existing label")

	next, _ := m.handleCommand("/label")
	tm := next.(Model)
	found := false
	for _, l := range tm.lines {
		if strings.Contains(l, "existing label") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected label shown: %v", tm.lines)
	}
}

func TestHandleCommand_Label_NoActiveSession(t *testing.T) {
	m := newTestModel()
	m.sessions = nil
	next, _ := m.handleCommand("/label hello")
	tm := next.(Model)
	found := false
	for _, l := range tm.lines {
		if strings.Contains(l, "not available") || strings.Contains(l, "No active") || strings.Contains(l, "store") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected error: %v", tm.lines)
	}
}
