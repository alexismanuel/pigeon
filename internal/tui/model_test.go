package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"pigeon/internal/config"
	"pigeon/internal/provider/openrouter"
	"pigeon/internal/resources"
	"pigeon/internal/session"
)

// ── pure helpers ──────────────────────────────────────────────────────────────

func TestSummarize_Empty(t *testing.T) {
	if got := summarize(""); got != "(no output)" {
		t.Errorf("expected '(no output)', got %q", got)
	}
}

func TestSummarize_Whitespace(t *testing.T) {
	if got := summarize("   \n  "); got != "(no output)" {
		t.Errorf("expected '(no output)', got %q", got)
	}
}

func TestSummarize_ShortLine(t *testing.T) {
	if got := summarize("hello world"); got != "hello world" {
		t.Errorf("unexpected: %q", got)
	}
}

func TestSummarize_TakesFirstLine(t *testing.T) {
	got := summarize("first line\nsecond line")
	if got != "first line" {
		t.Errorf("expected first line only, got %q", got)
	}
}

func TestSummarize_LongLineTruncated(t *testing.T) {
	long := strings.Repeat("a", 200)
	got := summarize(long)
	if len(got) > 123 { // 120 + "..."
		t.Errorf("expected truncation at 120 chars, got len=%d", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("expected trailing '...', got %q", got)
	}
}

func TestShortID_Short(t *testing.T) {
	if got := shortID("abc"); got != "abc" {
		t.Errorf("unexpected: %q", got)
	}
}

func TestShortID_Exactly12(t *testing.T) {
	id := "123456789012"
	if got := shortID(id); got != id {
		t.Errorf("12-char ID should be unchanged, got %q", got)
	}
}

func TestShortID_Long(t *testing.T) {
	id := "abcdefghijklmnopqrstuvwxyz"
	got := shortID(id)
	if len(got) != 12 {
		t.Errorf("expected 12 chars, got %d (%q)", len(got), got)
	}
}

func TestShortID_Whitespace(t *testing.T) {
	got := shortID("  hello  ")
	if got != "hello" {
		t.Errorf("expected trimmed 'hello', got %q", got)
	}
}

func TestLastAssistantContent_Empty(t *testing.T) {
	got := lastAssistantContent(nil)
	if got != "" {
		t.Errorf("expected empty for nil messages")
	}
}

func TestLastAssistantContent_NoAssistant(t *testing.T) {
	msgs := []openrouter.Message{{Role: "user", Content: "hi"}}
	if got := lastAssistantContent(msgs); got != "" {
		t.Errorf("expected empty when no assistant message")
	}
}

func TestLastAssistantContent_ReturnsLast(t *testing.T) {
	msgs := []openrouter.Message{
		{Role: "assistant", Content: "first"},
		{Role: "user", Content: "ok"},
		{Role: "assistant", Content: "second"},
	}
	if got := lastAssistantContent(msgs); got != "second" {
		t.Errorf("expected 'second', got %q", got)
	}
}

func TestLastAssistantContent_SkipsEmpty(t *testing.T) {
	msgs := []openrouter.Message{
		{Role: "assistant", Content: "real"},
		{Role: "assistant", Content: "   "},
	}
	if got := lastAssistantContent(msgs); got != "real" {
		t.Errorf("expected 'real', got %q", got)
	}
}

// ── stripBlankLines ───────────────────────────────────────────────────────────

func TestStripBlankLines_NoBlankLines(t *testing.T) {
	got := stripBlankLines("hello\nworld")
	if got != "hello\nworld" {
		t.Errorf("unexpected: %q", got)
	}
}

func TestStripBlankLines_RemovesInternalBlanks(t *testing.T) {
	got := stripBlankLines("para1\n\npara2")
	if got != "para1\npara2" {
		t.Errorf("expected blank line removed, got %q", got)
	}
}

func TestStripBlankLines_RemovesWhitespaceOnlyLines(t *testing.T) {
	got := stripBlankLines("a\n   \nb")
	if got != "a\nb" {
		t.Errorf("expected whitespace-only line removed, got %q", got)
	}
}

func TestStripBlankLines_EmptyInput(t *testing.T) {
	got := stripBlankLines("")
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

// ── appendHistoryBlocks ───────────────────────────────────────────────────────

func TestAppendHistoryBlocks_Empty(t *testing.T) {
	m := Model{}
	m.appendHistoryBlocks(nil)
	if len(m.chatBlocks) != 0 {
		t.Errorf("expected 0 blocks for empty history, got %d", len(m.chatBlocks))
	}
}

func TestAppendHistoryBlocks_UserAndAssistant(t *testing.T) {
	msgs := []openrouter.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
	}
	m := Model{}
	m.appendHistoryBlocks(msgs)
	// Each message has a bSep + content block = 4 blocks total.
	if len(m.chatBlocks) != 4 {
		t.Fatalf("expected 4 blocks (2 sep + 2 content), got %d", len(m.chatBlocks))
	}
	found := false
	for _, b := range m.chatBlocks {
		if strings.Contains(b.content, "hello") || strings.Contains(b.content, "hi there") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected blocks to contain message content")
	}
}

func TestAppendHistoryBlocks_ToolMessages(t *testing.T) {
	msgs := []openrouter.Message{
		{Role: "tool", Name: "bash", Content: "output here"},
	}
	m := Model{}
	m.appendHistoryBlocks(msgs)
	// bSep + bToolResult = 2 blocks
	if len(m.chatBlocks) != 2 {
		t.Fatalf("expected 2 blocks (sep + tool result), got %d", len(m.chatBlocks))
	}
	if m.chatBlocks[1].toolName != "bash" {
		t.Errorf("expected toolName='bash', got %q", m.chatBlocks[1].toolName)
	}
}

func TestAppendHistoryBlocks_AssistantToolCalls(t *testing.T) {
	msgs := []openrouter.Message{
		{
			Role: "assistant",
			ToolCalls: []openrouter.ToolCall{
				{Function: openrouter.ToolFunctionCall{Name: "read", Arguments: `{"path":"x"}`}},
			},
		},
	}
	m := Model{}
	m.appendHistoryBlocks(msgs)
	// bSep + bToolCall = 2 blocks
	if len(m.chatBlocks) != 2 {
		t.Fatalf("expected 2 blocks (sep + tool call), got %d", len(m.chatBlocks))
	}
	if m.chatBlocks[1].toolName != "read" {
		t.Errorf("expected toolName='read', got %q", m.chatBlocks[1].toolName)
	}
}

func TestAppendHistoryBlocks_EmptyContentSkipped(t *testing.T) {
	msgs := []openrouter.Message{
		{Role: "user", Content: "   "},
		{Role: "assistant", Content: ""},
	}
	m := Model{}
	m.appendHistoryBlocks(msgs)
	if len(m.chatBlocks) != 0 {
		t.Errorf("expected empty content to be skipped, got %d blocks", len(m.chatBlocks))
	}
}

// ── renderTree ────────────────────────────────────────────────────────────────

func makeNode(id, parentID, role, content string, t time.Time) session.Node {
	return session.Node{
		ID:         id,
		ParentID:   parentID,
		RecordedAt: t,
		Message:    openrouter.Message{Role: role, Content: content},
	}
}

func TestRenderLinearTree_SingleNode(t *testing.T) {
	now := time.Now()
	nodes := []session.Node{makeNode("aaa111", "", "user", "hello", now)}
	lines := renderTree(nodes, "aaa111")
	if len(lines) == 0 {
		t.Fatal("expected at least one line")
	}
	if !strings.Contains(lines[0], "*") {
		t.Errorf("current node should be marked with *: %q", lines[0])
	}
}

func TestRenderLinearTree_Chain(t *testing.T) {
	now := time.Now()
	nodes := []session.Node{
		makeNode("aaa", "", "user", "q1", now),
		makeNode("bbb", "aaa", "assistant", "a1", now.Add(time.Second)),
		makeNode("ccc", "bbb", "user", "q2", now.Add(2*time.Second)),
	}
	lines := renderTree(nodes, "ccc")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines for 3-node linear tree, got %d", len(lines))
	}
	// last node should be marked current
	if !strings.Contains(lines[2], "*") {
		t.Errorf("last node should be current: %q", lines[2])
	}
	// content summaries included
	if !strings.Contains(lines[0], "q1") {
		t.Errorf("first line should contain 'q1': %q", lines[0])
	}
}

func TestRenderTree_BranchedFallsBackToAscii(t *testing.T) {
	now := time.Now()
	// root with two children = branched
	nodes := []session.Node{
		makeNode("root", "", "user", "root", now),
		makeNode("branch1", "root", "assistant", "b1", now.Add(time.Second)),
		makeNode("branch2", "root", "assistant", "b2", now.Add(2*time.Second)),
	}
	lines := renderTree(nodes, "branch1")
	if len(lines) == 0 {
		t.Fatal("expected ascii tree output")
	}
	// ASCII tree uses connectors
	found := false
	for _, l := range lines {
		if strings.Contains(l, "├") || strings.Contains(l, "└") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected ASCII tree connectors, got: %v", lines)
	}
}

// ── buildResourceCmds ─────────────────────────────────────────────────────────

func TestBuildResourceCmds_NilRegistry(t *testing.T) {
	cmds := buildResourceCmds(nil, nil)
	if len(cmds) != 0 {
		t.Errorf("expected no commands from nil registry, got %d", len(cmds))
	}
}

func TestBuildResourceCmds_SkillsAndPrompts(t *testing.T) {
	global := t.TempDir()
	writeFile(t, global+"/skills/go-dev/SKILL.md", "go skill")
	writeFile(t, global+"/prompts/review.md", "review prompt")

	reg, err := resources.LoadFrom(global, "")
	if err != nil {
		t.Fatal(err)
	}

	cmds := buildResourceCmds(reg, nil)
	names := make(map[string]bool)
	for _, c := range cmds {
		names[c.name] = true
	}
	if !names["/skill:go-dev"] {
		t.Error("expected /skill:go-dev command")
	}
	if !names["/review"] {
		t.Error("expected /review command")
	}
}

// ── suggestion update via model.Update ───────────────────────────────────────

func newTestModel() Model {
	return NewModel(nil, nil, "test-model", nil, "", nil, nil, nil, config.Settings{}, nil)
}

func typeInto(m Model, chars string) Model {
	for _, ch := range chars {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
		m = next.(Model)
	}
	return m
}

func TestSuggestions_SlashShowsAll(t *testing.T) {
	m := typeInto(newTestModel(), "/")
	if len(m.suggestions) != len(builtinCommands) {
		t.Errorf("expected %d suggestions for '/', got %d", len(builtinCommands), len(m.suggestions))
	}
}

func TestSuggestions_FilteredByPrefix(t *testing.T) {
	m := typeInto(newTestModel(), "/mo")
	if len(m.suggestions) != 1 || m.suggestions[0].name != "/model" {
		t.Errorf("expected [/model], got %v", m.suggestions)
	}
}

func TestSuggestions_ClearedOnSpace(t *testing.T) {
	m := typeInto(newTestModel(), "/model ")
	if len(m.suggestions) != 0 {
		t.Errorf("expected suggestions cleared after space, got %d", len(m.suggestions))
	}
}

func TestSuggestions_ClearedOnNonSlash(t *testing.T) {
	m := typeInto(newTestModel(), "hello")
	if len(m.suggestions) != 0 {
		t.Errorf("expected no suggestions for normal text, got %d", len(m.suggestions))
	}
}

func TestSuggestions_NavWithArrowKeys(t *testing.T) {
	m := typeInto(newTestModel(), "/")
	if m.suggCursor != 0 {
		t.Fatalf("cursor should start at 0")
	}

	// down moves cursor
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(Model)
	if m.suggCursor != 1 {
		t.Errorf("expected cursor=1 after down, got %d", m.suggCursor)
	}

	// up moves it back
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = next.(Model)
	if m.suggCursor != 0 {
		t.Errorf("expected cursor=0 after up, got %d", m.suggCursor)
	}
}

func TestSuggestions_UpDoesNotGoNegative(t *testing.T) {
	m := typeInto(newTestModel(), "/")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = next.(Model)
	if m.suggCursor < 0 {
		t.Errorf("cursor went negative: %d", m.suggCursor)
	}
}

func TestSuggestions_DownDoesNotExceedLength(t *testing.T) {
	m := typeInto(newTestModel(), "/")
	total := len(m.suggestions)
	for i := 0; i < total+5; i++ {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = next.(Model)
	}
	if m.suggCursor >= total {
		t.Errorf("cursor exceeded list length: cursor=%d total=%d", m.suggCursor, total)
	}
}

func TestSuggestions_EscClears(t *testing.T) {
	m := typeInto(newTestModel(), "/mo")
	if len(m.suggestions) == 0 {
		t.Fatal("need suggestions before testing esc")
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	if len(m.suggestions) != 0 {
		t.Errorf("expected suggestions cleared after esc, got %d", len(m.suggestions))
	}
}

func TestSuggestions_TabFillsCommand(t *testing.T) {
	m := typeInto(newTestModel(), "/mo")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(Model)
	val := m.input.Value()
	// /model takes args so should have a trailing space
	if val != "/model " {
		t.Errorf("expected '/model ' after tab, got %q", val)
	}
	if len(m.suggestions) != 0 {
		t.Errorf("suggestions should clear after tab-fill")
	}
}

func TestSuggestions_EnterFillsCommandNotSubmit(t *testing.T) {
	m := typeInto(newTestModel(), "/mo")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	val := m.input.Value()
	if val != "/model " {
		t.Errorf("expected '/model ' after enter on suggestion, got %q", val)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// ── handleCommand ─────────────────────────────────────────────────────────────

func TestHandleCommand_Quit(t *testing.T) {
	m := newTestModel()
	_, cmd := m.handleCommand("/quit")
	if cmd == nil {
		t.Error("expected a quit command")
	}
}

func TestHandleCommand_UnknownCommand(t *testing.T) {
	m := newTestModel()
	next, _ := m.handleCommand("/doesnotexist")
	tm := next.(Model)
	found := false
	for _, l := range tm.lines {
		if strings.Contains(l, "unknown") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'unknown command' error line, got: %v", tm.lines)
	}
}

func TestHandleCommand_ModelDirect(t *testing.T) {
	m := newTestModel()
	next, _ := m.handleCommand("/model openai/gpt-4o")
	tm := next.(Model)
	if tm.modelName != "openai/gpt-4o" {
		t.Errorf("expected model='openai/gpt-4o', got %q", tm.modelName)
	}
}

func TestHandleCommand_NewSession_NilStore(t *testing.T) {
	m := newTestModel()
	next, _ := m.handleCommand("/new")
	tm := next.(Model)
	found := false
	for _, l := range tm.lines {
		if strings.Contains(l, "not available") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'not available' when sessions is nil: %v", tm.lines)
	}
}

func TestHandleCommand_SkillNotFound(t *testing.T) {
	m := newTestModel()
	next, _ := m.handleCommand("/skill:nonexistent")
	tm := next.(Model)
	found := false
	for _, l := range tm.lines {
		if strings.Contains(l, "skill not found") || strings.Contains(l, "no resource") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected error about skill not found: %v", tm.lines)
	}
}

// ── View / renderHeader / renderStatusBar ────────────────────────────────────

func TestView_ChatModeContainsInput(t *testing.T) {
	m := newTestModel()
	view := m.View()
	if !strings.Contains(view, "pigeon") {
		t.Errorf("expected header in view")
	}
}

func TestRenderHeader_PickerMode(t *testing.T) {
	m := newTestModel()
	m.mode = pickerMode
	h := m.renderHeader()
	if !strings.Contains(h, "picking") {
		t.Errorf("expected 'picking' status in header: %q", h)
	}
}

func TestRenderHeader_StreamingStatus(t *testing.T) {
	m := newTestModel()
	m.running = true
	h := m.renderHeader()
	if !strings.Contains(h, "streaming") {
		t.Errorf("expected 'streaming' status in header: %q", h)
	}
}

func TestRenderStatusBar_Empty(t *testing.T) {
	m := newTestModel()
	if bar := m.renderStatusBar(); bar != "" {
		t.Errorf("expected empty status bar, got %q", bar)
	}
}

func TestRenderStatusBar_WithStatuses(t *testing.T) {
	m := newTestModel()
	m.statuses["a"] = "status-a"
	m.statuses["b"] = "status-b"
	bar := m.renderStatusBar()
	if !strings.Contains(bar, "status-a") || !strings.Contains(bar, "status-b") {
		t.Errorf("expected both statuses in bar: %q", bar)
	}
}

func TestRenderSuggestions_HighlightsCurrent(t *testing.T) {
	m := typeInto(newTestModel(), "/")
	if len(m.suggestions) == 0 {
		t.Fatal("need suggestions")
	}
	out := m.renderSuggestions()
	if !strings.Contains(out, "▶") {
		t.Errorf("expected cursor marker ▶ in suggestions: %q", out)
	}
}
