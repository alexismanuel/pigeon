package tui

// Tests for the Update loop: message handling, picker mode, submitPrompt.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"pigeon/internal/agent"
	"pigeon/internal/config"
	luaext "pigeon/internal/extensions/lua"
	"pigeon/internal/provider/openrouter"
	"pigeon/internal/resources"
	"pigeon/internal/session"
)

// ── fakes ─────────────────────────────────────────────────────────────────────

type fakeAgent struct {
	response string
	err      error
}

func (f *fakeAgent) RunTurn(_ context.Context, _ string, _ []openrouter.Message, input string, cb agent.TurnCallbacks) ([]openrouter.Message, error) {
	if f.err != nil {
		return nil, f.err
	}
	if cb.OnToken != nil {
		cb.OnToken(f.response)
	}
	return []openrouter.Message{
		{Role: "user", Content: input},
		{Role: "assistant", Content: f.response},
	}, nil
}

func newModelWithSessions(t *testing.T) (Model, *session.Manager, string) {
	t.Helper()
	mgr := session.NewManager(t.TempDir())
	id, err := mgr.NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	m := NewModel(nil, nil, "test-model", mgr, id, nil, nil, nil, config.Settings{}, nil, nil)
	return m, mgr, id
}

// streamedModel drives the stream channel until the turn is done and returns
// the final model. Caps at 64 iterations to avoid infinite loops in tests.
func streamedModel(t *testing.T, m Model, initCmd tea.Cmd) Model {
	t.Helper()
	cmd := initCmd
	for range 64 {
		if cmd == nil {
			return m
		}
		msg := cmd()
		if msg == nil {
			return m
		}
		next, nextCmd := m.Update(msg)
		m = next.(Model)
		cmd = nextCmd
		if _, done := msg.(turnDoneMsg); done {
			return m
		}
		if _, done := msg.(turnErrMsg); done {
			return m
		}
	}
	t.Error("stream did not complete within iteration limit")
	return m
}

// ── Init ──────────────────────────────────────────────────────────────────────

func TestInit_ReturnsBlink(t *testing.T) {
	m := newTestModel()
	cmd := m.Init()
	if cmd == nil {
		t.Error("Init should return at least textinput.Blink")
	}
}

func TestInit_WithRuntime(t *testing.T) {
	ch := make(chan luaext.StatusUpdate, 8)
	rt := luaext.NewRuntime(ch)
	defer rt.Close()
	rt.LoadString("t", `pigeon.on("session_start", function() pigeon.set_status("x","fired") end)`)

	m := NewModel(nil, nil, "m", nil, "", nil, rt, ch, config.Settings{}, nil, nil)
	cmd := m.Init()
	if cmd == nil {
		t.Error("Init with runtime should return a batch cmd")
	}
}

// ── statusUpdateMsg ───────────────────────────────────────────────────────────

func TestUpdate_StatusSet(t *testing.T) {
	m := newTestModel()
	next, cmd := m.Update(statusUpdateMsg{ID: "ext", Text: "hello"})
	tm := next.(Model)
	if tm.statuses["ext"] != "hello" {
		t.Errorf("expected status 'hello', got %q", tm.statuses["ext"])
	}
	// waitForStatus should be re-queued
	if cmd == nil {
		t.Error("expected waitForStatus to be re-queued")
	}
}

func TestUpdate_StatusClear(t *testing.T) {
	m := newTestModel()
	m.statuses["ext"] = "something"
	next, _ := m.Update(statusUpdateMsg{ID: "ext", Text: ""})
	tm := next.(Model)
	if _, ok := tm.statuses["ext"]; ok {
		t.Error("status should be cleared on empty text")
	}
}

// ── tokenMsg ──────────────────────────────────────────────────────────────────

func runningModel() Model {
	m := newTestModel()
	m.running = true
	m.streamingAssistantIdx = -1
	m.streamCh = make(chan tea.Msg, 32)
	return m
}

func TestUpdateChat_TokenAppendsToLine(t *testing.T) {
	m := runningModel()
	defer close(m.streamCh)

	next, _ := m.Update(tokenMsg{token: "world"})
	tm := next.(Model)
	if tm.streamingAssistantIdx < 0 {
		t.Fatal("streamingAssistantIdx not set after token")
	}
	if !strings.Contains(tm.lines[tm.streamingAssistantIdx], "world") {
		t.Errorf("token not in assistant line: %q", tm.lines[tm.streamingAssistantIdx])
	}
}

func TestUpdateChat_TokenCreatesNewLineWhenNoneActive(t *testing.T) {
	m := newTestModel()
	m.running = true
	m.streamingAssistantIdx = -1
	m.streamCh = make(chan tea.Msg, 32)
	defer close(m.streamCh)
	before := len(m.lines)

	next, _ := m.Update(tokenMsg{token: "hi"})
	tm := next.(Model)
	// We add a bSep + bAssistant block = 2 new entries.
	if len(tm.lines) != before+2 {
		t.Errorf("expected 2 new lines (separator+block); before=%d after=%d", before, len(tm.lines))
	}
	if tm.streamingAssistantIdx < 0 {
		t.Error("streamingAssistantIdx should be set after token on empty state")
	}
}

func TestUpdateChat_MultipleTokensAccumulate(t *testing.T) {
	m := runningModel()
	defer close(m.streamCh)

	for _, tok := range []string{"foo", " ", "bar"} {
		next, _ := m.Update(tokenMsg{token: tok})
		m = next.(Model)
	}
	if m.streamingAssistantIdx < 0 {
		t.Fatal("streamingAssistantIdx not set")
	}
	if !strings.Contains(m.lines[m.streamingAssistantIdx], "foo bar") {
		t.Errorf("expected accumulated tokens, got %q", m.lines[m.streamingAssistantIdx])
	}
}

// ── toolCallMsg ───────────────────────────────────────────────────────────────

func TestUpdateChat_ToolCallResetsAssistantLine(t *testing.T) {
	m := runningModel()
	defer close(m.streamCh)

	next, _ := m.Update(toolCallMsg{name: "bash", args: `{"command":"ls"}`})
	tm := next.(Model)
	if tm.streamingAssistantIdx != -1 {
		t.Error("streamingAssistantIdx should be -1 after tool call")
	}
}

func TestUpdateChat_ToolCallAddsLine(t *testing.T) {
	m := runningModel()
	defer close(m.streamCh)

	next, _ := m.Update(toolCallMsg{name: "read", args: `{"path":"x"}`})
	tm := next.(Model)
	found := false
	for _, l := range tm.lines {
		if strings.Contains(l, "read") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected tool call line with 'read': %v", tm.lines)
	}
}

// ── toolResultMsg ─────────────────────────────────────────────────────────────

func TestUpdateChat_ToolResultAddsLine(t *testing.T) {
	m := runningModel()
	defer close(m.streamCh)

	next, _ := m.Update(toolResultMsg{name: "read", result: "file-contents"})
	tm := next.(Model)
	found := false
	for _, l := range tm.lines {
		if strings.Contains(l, "file-contents") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected tool result content in lines: %v", tm.lines)
	}
}

func TestUpdateChat_ToolCallThenResult_IconFlips(t *testing.T) {
	m := runningModel()
	defer close(m.streamCh)

	// First, a tool call is emitted (streaming=true → running icon).
	next, _ := m.Update(toolCallMsg{name: "bash", args: `{"command":"ls"}`})
	tm := next.(Model)
	callIdx := -1
	for i, b := range tm.chatBlocks {
		if b.kind == bToolCall && b.toolName == "bash" {
			callIdx = i
			break
		}
	}
	if callIdx < 0 {
		t.Fatal("bToolCall block not found")
	}
	if !tm.chatBlocks[callIdx].streaming {
		t.Error("bToolCall should have streaming=true while running")
	}

	// Then the result arrives — the bToolCall header should flip to success.
	next2, _ := tm.Update(toolResultMsg{name: "bash", result: "output"})
	tm2 := next2.(Model)
	if tm2.chatBlocks[callIdx].streaming {
		t.Error("bToolCall should have streaming=false after result")
	}
	if tm2.chatBlocks[callIdx].isErr {
		t.Error("bToolCall should not be isErr for a successful result")
	}
}

func TestUpdateChat_ToolResultError(t *testing.T) {
	m := runningModel()
	defer close(m.streamCh)

	next, _ := m.Update(toolResultMsg{name: "bash", err: fmt.Errorf("cmd failed")})
	tm := next.(Model)
	found := false
	for _, l := range tm.lines {
		if strings.Contains(l, "cmd failed") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected error line: %v", tm.lines)
	}
}

// ── turnDoneMsg ───────────────────────────────────────────────────────────────

func TestUpdateChat_TurnDone_ClearsRunning(t *testing.T) {
	m := runningModel()
	msgs := []openrouter.Message{
		{Role: "user", Content: "q"},
		{Role: "assistant", Content: "a"},
	}
	next, _ := m.Update(turnDoneMsg{newMessages: msgs})
	tm := next.(Model)
	if tm.running {
		t.Error("running should be false after turn done")
	}
	if tm.streamingAssistantIdx != -1 {
		t.Error("streamingAssistantIdx should be -1 after turn done")
	}
}

func TestUpdateChat_TurnDone_AppendsHistory(t *testing.T) {
	m := runningModel()
	msgs := []openrouter.Message{
		{Role: "user", Content: "q"},
		{Role: "assistant", Content: "a"},
	}
	next, _ := m.Update(turnDoneMsg{newMessages: msgs})
	tm := next.(Model)
	if len(tm.history) != 2 {
		t.Errorf("expected 2 history messages, got %d", len(tm.history))
	}
}

func TestUpdateChat_TurnDone_FillsAssistantBlock(t *testing.T) {
	m := runningModel()
	// Simulate a streaming assistant block with no tokens yet.
	m.appendBlock(chatBlock{kind: bSep})
	m.streamingAssistantIdx = m.appendBlock(chatBlock{kind: bAssistant, streaming: true})
	msgs := []openrouter.Message{{Role: "assistant", Content: "final answer"}}
	next, _ := m.Update(turnDoneMsg{newMessages: msgs})
	tm := next.(Model)
	// Check the block content directly (lines contain ANSI codes from rendering).
	found := false
	for _, b := range tm.chatBlocks {
		if b.kind == bAssistant && strings.Contains(b.content, "final answer") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("assistant block content should contain 'final answer' after turnDone")
	}
}

// ── turnErrMsg ────────────────────────────────────────────────────────────────

func TestUpdateChat_TurnErr(t *testing.T) {
	m := runningModel()
	next, _ := m.Update(turnErrMsg{err: fmt.Errorf("network error")})
	tm := next.(Model)
	if tm.running {
		t.Error("running should be false after error")
	}
	if tm.streamingAssistantIdx != -1 {
		t.Error("streamingAssistantIdx should be -1")
	}
	found := false
	for _, l := range tm.lines {
		if strings.Contains(l, "network error") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected error in lines: %v", tm.lines)
	}
}

// ── extCommandDoneMsg ─────────────────────────────────────────────────────────

func TestUpdateChat_ExtCommandDone_NoError(t *testing.T) {
	m := newTestModel()
	next, _ := m.Update(extCommandDoneMsg{err: nil})
	tm := next.(Model)
	// no error line added
	for _, l := range tm.lines[2:] { // skip intro lines
		if strings.Contains(l, "error") {
			t.Errorf("unexpected error line: %q", l)
		}
	}
}

func TestUpdateChat_ExtCommandDone_WithError(t *testing.T) {
	m := newTestModel()
	next, _ := m.Update(extCommandDoneMsg{err: fmt.Errorf("lua panic")})
	tm := next.(Model)
	found := false
	for _, l := range tm.lines {
		if strings.Contains(l, "lua panic") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected error line: %v", tm.lines)
	}
}

// ── submitPrompt ──────────────────────────────────────────────────────────────

func TestSubmitPrompt_SetsRunning(t *testing.T) {
	m := NewModel(&fakeAgent{response: "hello"}, nil, "test-model", nil, "", nil, nil, nil, config.Settings{}, nil, nil)
	next, cmd := m.submitPrompt("say hello")
	tm := next.(Model)
	if !tm.running {
		t.Error("expected running=true")
	}
	if cmd == nil {
		t.Error("expected stream cmd")
	}
}

func TestSubmitPrompt_AddsUserAndAssistantLines(t *testing.T) {
	m := NewModel(&fakeAgent{response: "hello"}, nil, "test-model", nil, "", nil, nil, nil, config.Settings{}, nil, nil)
	next, cmd := m.submitPrompt("say hello")
	m = next.(Model)
	m = streamedModel(t, m, cmd)

	foundUser := false
	for _, l := range m.lines {
		if strings.Contains(l, "say hello") {
			foundUser = true
		}
	}
	if !foundUser {
		t.Errorf("expected user message in lines: %v", m.lines)
	}
}

func TestSubmitPrompt_NilAgentAddsError(t *testing.T) {
	m := newTestModel() // agent is nil
	next, _ := m.submitPrompt("hello")
	tm := next.(Model)
	found := false
	for _, l := range tm.lines {
		if strings.Contains(l, "agent not initialized") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'agent not initialized' error: %v", tm.lines)
	}
}

// ── updatePicker ──────────────────────────────────────────────────────────────

func pickerModel() Model {
	m := newTestModel()
	m.mode = pickerMode
	m.picker = newPicker(80, 24, nil)
	return m
}

func TestUpdatePicker_ModelPicked(t *testing.T) {
	m := pickerModel()
	next, _ := m.Update(modelPickedMsg{modelID: "openai/gpt-4o"})
	tm := next.(Model)
	if tm.mode != chatMode {
		t.Error("expected chat mode after pick")
	}
	if tm.modelName != "openai/gpt-4o" {
		t.Errorf("expected gpt-4o, got %q", tm.modelName)
	}
}

func TestUpdatePicker_Cancelled(t *testing.T) {
	m := pickerModel()
	m.modelName = "original-model"
	next, _ := m.Update(modelPickCanceledMsg{})
	tm := next.(Model)
	if tm.mode != chatMode {
		t.Error("expected chat mode after cancel")
	}
	if tm.modelName != "original-model" {
		t.Error("model should be unchanged after cancel")
	}
}

func TestUpdatePicker_LoadedMsg_RoutedToPicker(t *testing.T) {
	m := pickerModel()
	models := []openrouter.ModelInfo{
		{ID: "a/b", Name: "Model B"},
		{ID: "c/d", Name: "Model D"},
	}
	next, _ := m.Update(modelLoadedMsg{models: models})
	tm := next.(Model)
	if tm.picker.loading {
		t.Error("picker should not be loading after models loaded")
	}
	if len(tm.picker.filtered) != 2 {
		t.Errorf("expected 2 filtered models, got %d", len(tm.picker.filtered))
	}
}

func TestUpdatePicker_WindowResize(t *testing.T) {
	m := pickerModel()
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	tm := next.(Model)
	if tm.width != 120 || tm.height != 40 {
		t.Errorf("expected 120x40, got %dx%d", tm.width, tm.height)
	}
}

// ── handleCommand with real sessions ─────────────────────────────────────────

func TestHandleCommand_New_WithSessionStore(t *testing.T) {
	m, _, oldID := newModelWithSessions(t)
	next, _ := m.handleCommand("/new")
	tm := next.(Model)
	if tm.sessionID == oldID {
		t.Error("expected new session ID")
	}
	if len(tm.history) != 0 {
		t.Error("history should be empty for new session")
	}
}

func TestHandleCommand_Sessions_OpensPicker(t *testing.T) {
	m, _, _ := newModelWithSessions(t)
	next, cmd := m.handleCommand("/sessions")
	tm := next.(Model)
	if tm.mode != resumeMode {
		t.Errorf("expected resumeMode after /sessions, got %d", tm.mode)
	}
	if cmd == nil {
		t.Error("expected fetchSessions cmd")
	}
}

func TestHandleCommand_Tree_NoSession(t *testing.T) {
	m := newTestModel()
	m.sessions = nil
	next, _ := m.handleCommand("/tree")
	tm := next.(Model)
	found := false
	for _, l := range tm.lines {
		if strings.Contains(l, "not available") || strings.Contains(l, "No active") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected error/message for /tree with no store: %v", tm.lines)
	}
}

func TestHandleCommand_Tree_WithSession(t *testing.T) {
	m, mgr, id := newModelWithSessions(t)
	mgr.AppendMessages(id, "", []openrouter.Message{{Role: "user", Content: "q"}})
	m.currentNodeID = id // give it a node context

	next, _ := m.handleCommand("/tree")
	tm := next.(Model)
	if tm.mode != nodePickMode {
		t.Errorf("expected nodePickMode after /tree, got mode=%d lines=%v", tm.mode, tm.lines)
	}
}

func TestHandleCommand_SkillWithRegistry(t *testing.T) {
	global := t.TempDir()
	writeFile(t, global+"/skills/py/SKILL.md", "be a python expert")
	reg, _ := resources.LoadFrom(global, "")

	m := NewModel(nil, nil, "m", nil, "", reg, nil, nil, config.Settings{}, nil, nil)
	next, _ := m.handleCommand("/skill:py")
	tm := next.(Model)
	found := false
	for _, msg := range tm.history {
		if msg.Role == "system" && strings.Contains(msg.Content, "python expert") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected skill content in history: %v", tm.history)
	}
}

func TestHandleCommand_PromptExpansion(t *testing.T) {
	global := t.TempDir()
	writeFile(t, global+"/prompts/review.md", "Please review the following code:")
	reg, _ := resources.LoadFrom(global, "")

	m := NewModel(nil, nil, "m", nil, "", reg, nil, nil, config.Settings{}, nil, nil)
	next, _ := m.handleCommand("/review")
	tm := next.(Model)
	if !strings.Contains(tm.input.Value(), "Please review") {
		t.Errorf("expected prompt in input, got %q", tm.input.Value())
	}
}

// ── cmd helpers ───────────────────────────────────────────────────────────────

func TestWaitForStream_ReturnsMessage(t *testing.T) {
	ch := make(chan tea.Msg, 1)
	ch <- tokenMsg{token: "hello"}
	cmd := waitForStream(ch)
	msg := cmd()
	tok, ok := msg.(tokenMsg)
	if !ok || tok.token != "hello" {
		t.Errorf("expected tokenMsg{hello}, got %v", msg)
	}
}

func TestWaitForStream_ClosedChannelReturnsNil(t *testing.T) {
	ch := make(chan tea.Msg)
	close(ch)
	cmd := waitForStream(ch)
	if cmd() != nil {
		t.Error("closed channel should return nil")
	}
}

func TestWaitForStatus_ReturnsUpdate(t *testing.T) {
	ch := make(chan luaext.StatusUpdate, 1)
	ch <- luaext.StatusUpdate{ID: "x", Text: "y"}
	cmd := waitForStatus(ch)
	msg := cmd()
	upd, ok := msg.(statusUpdateMsg)
	if !ok || upd.ID != "x" || upd.Text != "y" {
		t.Errorf("unexpected msg: %v", msg)
	}
}

func TestWaitForStatus_ClosedChannelReturnsNil(t *testing.T) {
	ch := make(chan luaext.StatusUpdate)
	close(ch)
	if waitForStatus(ch)() != nil {
		t.Error("expected nil for closed channel")
	}
}

func TestFireEventCmd_ExecutesHandler(t *testing.T) {
	statusCh := make(chan luaext.StatusUpdate, 4)
	rt := luaext.NewRuntime(statusCh)
	defer rt.Close()
	rt.LoadString("t", `pigeon.on("turn_end", function() pigeon.set_status("k","v") end)`)

	cmd := fireEventCmd(rt, luaext.Event{Kind: luaext.EventTurnEnd})
	cmd() // run the goroutine synchronously

	select {
	case upd := <-statusCh:
		if upd.ID != "k" || upd.Text != "v" {
			t.Errorf("unexpected update: %+v", upd)
		}
	default:
		t.Error("expected status update after firing event")
	}
}

func TestRunExtCommandCmd_Success(t *testing.T) {
	statusCh := make(chan luaext.StatusUpdate, 4)
	rt := luaext.NewRuntime(statusCh)
	defer rt.Close()
	rt.LoadString("t", `pigeon.register_command("ping", "pong", function() pigeon.set_status("p","pong") end)`)

	cmd := runExtCommandCmd(rt, "ping", "")
	msg := cmd()
	done, ok := msg.(extCommandDoneMsg)
	if !ok {
		t.Fatalf("expected extCommandDoneMsg, got %T", msg)
	}
	if done.err != nil {
		t.Errorf("unexpected error: %v", done.err)
	}
}

func TestRunExtCommandCmd_UnknownCommand(t *testing.T) {
	rt := luaext.NewRuntime(nil)
	defer rt.Close()
	cmd := runExtCommandCmd(rt, "nope", "")
	msg := cmd()
	done, ok := msg.(extCommandDoneMsg)
	if !ok {
		t.Fatalf("expected extCommandDoneMsg")
	}
	if done.err == nil {
		t.Error("expected error for unknown command")
	}
}



// ── viewport / scroll ─────────────────────────────────────────────────────────

func TestUpdate_WindowSizeMsg_SetsViewportDimensions(t *testing.T) {
	m := newTestModel()
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	tm := next.(Model)
	if tm.vp.Width != 120 {
		t.Errorf("expected vp.Width=120, got %d", tm.vp.Width)
	}
	if tm.vp.Height <= 0 {
		t.Errorf("expected positive vp.Height, got %d", tm.vp.Height)
	}
}

func TestUpdate_MouseWheelUp_DisablesAutoScroll(t *testing.T) {
	m := newTestModel()
	// give it a real size so viewport is usable
	next0, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next0.(Model)
	next, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	tm := next.(Model)
	if tm.autoScroll {
		t.Error("autoScroll should be false after wheel up")
	}
}

func TestUpdate_MouseWheelDown_AtBottomReenablesAutoScroll(t *testing.T) {
	m := newTestModel()
	m.autoScroll = false
	next1, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next1.(Model)
	// wheel down when already at bottom → should re-enable autoScroll
	next, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	tm := next.(Model)
	if !tm.autoScroll {
		t.Error("autoScroll should be re-enabled when viewport is at bottom")
	}
}

func TestRecalcViewport_ChromeIncludesSuggestions(t *testing.T) {
	m := newTestModel()
	m.height = 30
	m.width = 80
	m.suggestions = make([]commandDef, 3)

	plain := m.recalcViewport()
	if plain.vp.Height != max(3, 30-4-3) {
		t.Errorf("unexpected height with 3 suggestions: %d", plain.vp.Height)
	}
}

func TestAutoScroll_NewContentScrollsDown(t *testing.T) {
	m := NewModel(&fakeAgent{response: "hello world"}, nil, "test-model", nil, "", nil, nil, nil, config.Settings{}, nil, nil)
	m.autoScroll = true
	next2, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next2.(Model)

	// fill viewport with many lines so there's actually something to scroll
	for range 40 {
		m.lines = append(m.lines, "line")
	}
	// trigger sync via any update
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	tm := next.(Model)
	if !tm.vp.AtBottom() {
		t.Error("autoScroll=true should keep viewport at bottom after content added")
	}
}

func TestHandleCommand_System_Set(t *testing.T) {
	m := newTestModel()
	next, _ := m.handleCommand("/system You are a helpful assistant")
	tm := next.(Model)
	if tm.systemPrompt != "You are a helpful assistant" {
		t.Errorf("expected system prompt set, got %q", tm.systemPrompt)
	}
	found := false
	for _, l := range tm.lines {
		if strings.Contains(l, "updated") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected confirmation line: %v", tm.lines)
	}
}

func TestHandleCommand_System_ShowEmpty(t *testing.T) {
	m := newTestModel()
	next, _ := m.handleCommand("/system")
	tm := next.(Model)
	found := false
	for _, l := range tm.lines {
		if strings.Contains(l, "No system prompt") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'No system prompt' message: %v", tm.lines)
	}
}

func TestHandleCommand_System_ShowCurrent(t *testing.T) {
	m := newTestModel()
	m.systemPrompt = "Be concise."
	next, _ := m.handleCommand("/system")
	tm := next.(Model)
	found := false
	for _, l := range tm.lines {
		if strings.Contains(l, "Be concise.") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected current prompt in output: %v", tm.lines)
	}
}

func TestNewModel_SystemPromptVariadic(t *testing.T) {
	m := NewModel(nil, nil, "model", nil, "", nil, nil, nil, config.Settings{}, nil, nil, "You are a pirate.")
	if m.systemPrompt != "You are a pirate." {
		t.Errorf("expected system prompt, got %q", m.systemPrompt)
	}
}

func TestNewModel_NoSystemPrompt(t *testing.T) {
	m := NewModel(nil, nil, "model", nil, "", nil, nil, nil, config.Settings{}, nil, nil)
	if m.systemPrompt != "" {
		t.Errorf("expected empty system prompt, got %q", m.systemPrompt)
	}
}

// ── pruneCurrentSessionIfEmpty ────────────────────────────────────────────────

func TestPruneCurrentSessionIfEmpty_DeletesEmptySession(t *testing.T) {
	m, mgr, id := newModelWithSessions(t)

	// Sanity: session exists before prune.
	sessions, _ := mgr.ListSessions(0)
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session before prune, got %d", len(sessions))
	}

	m.pruneCurrentSessionIfEmpty()

	sessions, _ = mgr.ListSessions(0)
	if len(sessions) != 0 {
		t.Errorf("empty session should have been deleted, got %d remaining", len(sessions))
	}
	_ = id
}

func TestPruneCurrentSessionIfEmpty_KeepsSessionWithMessages(t *testing.T) {
	m, mgr, id := newModelWithSessions(t)
	mgr.AppendMessages(id, "", []openrouter.Message{{Role: "user", Content: "hello"}})

	m.pruneCurrentSessionIfEmpty()

	sessions, _ := mgr.ListSessions(0)
	if len(sessions) != 1 {
		t.Errorf("non-empty session should be kept, got %d remaining", len(sessions))
	}
}

func TestPruneCurrentSessionIfEmpty_NoOpWhenNoStore(t *testing.T) {
	m := newTestModel() // sessions == nil
	// Should not panic.
	m.pruneCurrentSessionIfEmpty()
}

func TestPruneCurrentSessionIfEmpty_NoOpWhenNoSessionID(t *testing.T) {
	mgr := session.NewManager(t.TempDir())
	m := NewModel(nil, nil, "test-model", mgr, "", nil, nil, nil, config.Settings{}, nil, nil)
	// Should not panic and should not delete anything.
	m.pruneCurrentSessionIfEmpty()
}
