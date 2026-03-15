package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"pigeon/internal/agent"
	"pigeon/internal/config"
	luaext "pigeon/internal/extensions/lua"
	"pigeon/internal/permission"
	"pigeon/internal/provider/openrouter"
	"pigeon/internal/resources"
	"pigeon/internal/session"
)

var (
	suggSelectedStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	suggSelectedDimStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	suggNormalStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	suggDimStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

type appMode int

const (
	chatMode       appMode = iota
	pickerMode     appMode = iota
	resumeMode     appMode = iota
	nodePickMode   appMode = iota
	permissionMode appMode = iota
)

// permDialogChrome is the number of terminal lines the permission dialog
// occupies below the viewport (replaces the normal input + status chrome).
const permDialogChrome = 11

type turnRunner interface {
	RunTurn(ctx context.Context, model string, history []openrouter.Message, userInput string, cb agent.TurnCallbacks) ([]openrouter.Message, error)
}

type sessionStore interface {
	NewSession() (string, error)
	AppendMessages(sessionID, parentNodeID string, messages []openrouter.Message) (string, error)
	LoadLatestMessages(sessionID string) ([]openrouter.Message, string, error)
	LoadMessagesAtNode(sessionID, nodeID string) ([]openrouter.Message, error)
	ResolveNodeID(sessionID, prefix string) (string, error)
	ListNodes(sessionID string) ([]session.Node, error)
	ListSessions(limit int) ([]session.SessionMeta, error)
	SetSessionModel(sessionID, model string) error
	GetSessionModel(sessionID string) (string, error)
	SetSessionLabel(sessionID, label string) error
	GetSessionLabel(sessionID string) (string, error)
	GetFirstUserMessage(sessionID string) (string, error)
	DeleteSession(sessionID string) error
}

type modelCatalog interface {
	ListModels(ctx context.Context) ([]openrouter.ModelInfo, error)
}

// ── chat event messages ────────────────────────────────────────────────────────

type tokenMsg struct {
	token string
}

type thinkingTokenMsg struct {
	token string
}

// blockKind identifies the kind of a chatBlock.
type blockKind uint8

const (
	bSep        blockKind = iota // blank separator line between messages
	bMeta                        // dim system text (session info, status)
	bError                       // red error text
	bShell                       // yellow shell output (legacy single-line)
	bShellBlock                  // shell command + output combined block (user-message style)
	bUser                        // user message
	bAssistant                   // assistant message
	bThinking                    // thinking block (collapsible)
	bToolCall                    // tool invocation
	bToolResult                  // tool result (collapsible)
)

// chatBlock is one display unit in the chat. m.chatBlocks and m.lines are kept
// in strict 1:1 correspondence: m.lines[i] = m.renderBlock(m.chatBlocks[i]).
type chatBlock struct {
	kind     blockKind
	content  string // display text / tool result body
	command  string // bShellBlock: the shell command that was run
	toolName string // bToolCall, bToolResult
	toolArgs string // bToolCall
	isErr    bool   // bToolResult / bShellBlock: true = error result
	// collapsed applies to bThinking and bToolResult.
	collapsed bool
	// streaming is true for bAssistant, bThinking, bShellBlock while output is
	// still arriving; it switches to false when done.
	streaming bool
}

// toolResultPreviewLines is the number of lines shown in a collapsed tool result.
const toolResultPreviewLines = 5

type toolCallMsg struct {
	name string
	args string
}

type toolResultMsg struct {
	name   string
	result string
	err    error
}

type turnDoneMsg struct {
	newMessages []openrouter.Message
}

type turnErrMsg struct {
	err error
}

type statusUpdateMsg luaext.StatusUpdate
type extCommandDoneMsg struct{ err error }

// ── main model ─────────────────────────────────────────────────────────────────

type Model struct {
	agent    turnRunner
	sessions sessionStore
	catalog  modelCatalog

	mode          appMode
	picker        picker
	sessionPicker sessionPicker
	nodePicker    nodePicker

	sessionID     string
	currentNodeID string
	history       []openrouter.Message
	modelName     string
	systemPrompt  string // injected as first message each turn; "" = none

	input      textinput.Model
	vp         viewport.Model
	autoScroll bool // scroll to bottom whenever new content arrives

	// chatBlocks is the source of truth for all displayed content.
	// lines is its rendered cache: lines[i] = renderBlock(chatBlocks[i]).
	// They are always kept in strict 1:1 correspondence.
	chatBlocks []chatBlock
	lines      []string

	streamCh   chan tea.Msg
	running    bool
	cancelTurn context.CancelFunc // non-nil while a turn is in flight
	width      int
	height     int

	// Indices into chatBlocks for the currently streaming items; -1 = none.
	streamingAssistantIdx int
	streamingThinkingIdx  int

	thinkingCollapsed    bool // global collapse state for thinking blocks
	toolResultsCollapsed bool // global collapse state for tool results
	spinner          spinner.Model

	registry     *resources.Registry
	resourceCmds []commandDef // dynamic commands built from registry + extension commands

	// permission dialog
	permService permission.Service   // nil when permissions are disabled
	currentPerm *permission.Request  // non-nil while a permission dialog is active

	shellCh               chan tea.Msg // receives shellOutputMsg / shellDoneMsg from background shell
	shellBlockIdx         int         // index of the active bShellBlock, -1 = none
	shellCmdParentNodeID  string      // currentNodeID captured at shell command start, for session recording
	shellCompletionBase string     // part of input before the path prefix being completed

	mdRenderer  *glamour.TermRenderer // created once at init, before BubbleTea owns stdin
	mdStyle     string                // "dark" or "light", detected at init

	keybindings config.Keybindings

	runtime  *luaext.Runtime
	statusCh <-chan luaext.StatusUpdate
	statuses map[string]string // id → text, from extension set_status calls

	suggestions []commandDef
	suggCursor  int
}

var (
	headerStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	errorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	shellStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("11")) // yellow for shell output
	metaStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	thinkingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)

	toolResultToggleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)

	// msgBlockPrefix is the number of terminal columns consumed by the
	// left border character (1) plus the padding space (1).
	// This mirrors Crush's MessageLeftPaddingTotal = 2.
	msgBlockPrefix = 2

	// User block: thick left border, no gap between bar and bg.
	userBorderPrefix = lipgloss.NewStyle().
				BorderStyle(lipgloss.ThickBorder()).
				BorderLeft(true).
				BorderForeground(lipgloss.Color("60"))

	// Tool status icons — Crush-style single-char status on the header line.
	toolIconRunning = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).SetString("●")
	toolIconSuccess = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).SetString("✓")
	toolIconError   = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).SetString("✗")

	// Tool text styles.
	toolNameStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	toolArgsStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	toolTruncStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)

	bgUser  = lipgloss.Color("#131a2e")
	bgShell = lipgloss.Color("#141414") // near-black for shell blocks

	shellBlockBorderPrefix = lipgloss.NewStyle().
				BorderStyle(lipgloss.ThickBorder()).
				BorderLeft(true).
				BorderForeground(lipgloss.Color("240")) // medium grey

	// permission dialog styles
	permTitleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("11"))
	permLabelStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	permValueStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	permBorderStyle  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("11")).Padding(0, 1)
	permCodeStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("235")).Padding(0, 1)
	permDiffAddStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	permDiffDelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	permAllowStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10"))
	permSessionStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("11"))
	permDenyStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9"))
	permHelpStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

// permRequestMsg is sent to the TUI when a permission request arrives.
// shellOutputMsg carries a chunk of stdout/stderr from a ! command.
type shellOutputMsg struct{ text string }

// shellDoneMsg is sent when a ! command exits.
type shellDoneMsg struct{ err error }

type permRequestMsg struct {
	req permission.Request
}

func NewModel(ag turnRunner, catalog modelCatalog, modelName string, sessions sessionStore, sessionID string, reg *resources.Registry, rt *luaext.Runtime, statusCh <-chan luaext.StatusUpdate, settings config.Settings, perm permission.Service, systemPrompt ...string) Model {
	in := textinput.New()
	in.Placeholder = "Ask pigeon..."
	in.Prompt = "> "
	in.Focus()
	in.CharLimit = 0
	in.Width = 100

	vp := viewport.New(0, 0)
	vp.KeyMap = viewport.KeyMap{} // disable all keyboard bindings; mouse wheel only

	// Detect dark/light style NOW, before BubbleTea takes over stdin.
	// WithAutoStyle() sends an OSC 11 terminal query; if we did this later the
	// terminal's response would be read by BubbleTea as keyboard input and
	// appear verbatim in the text field.
	mdStyle := glamourStyle()
	mdRenderer, _ := glamour.NewTermRenderer(
		glamour.WithStandardStyle(mdStyle),
		glamour.WithWordWrap(78), // 80 - msgBlockPrefix(2)
	)

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))

	m := Model{
		agent:                 ag,
		catalog:               catalog,
		sessions:              sessions,
		sessionID:             strings.TrimSpace(sessionID),
		modelName:             strings.TrimSpace(modelName),
		input:                 in,
		vp:                    vp,
		autoScroll:            true,
		streamingAssistantIdx: -1,
		streamingThinkingIdx:  -1,
		shellBlockIdx:         -1,
		thinkingCollapsed:     settings.CollapseThinking,
		spinner:               sp,
		mdRenderer:            mdRenderer,
		mdStyle:               mdStyle,
		keybindings:           settings.Keybindings,
		registry:              reg,
		runtime:               rt,
		statusCh:              statusCh,
		statuses:              make(map[string]string),
		resourceCmds:          buildResourceCmds(reg, rt),
		permService:           perm,
	}
	if len(systemPrompt) > 0 {
		m.systemPrompt = strings.TrimSpace(systemPrompt[0])
	}

	// Populate intro blocks (shown before any session content).
	m.appendIntroBlocks(strings.TrimSpace(sessionID), "")

	if m.sessions != nil && m.sessionID != "" {
		if savedModel, err := m.sessions.GetSessionModel(m.sessionID); err == nil && strings.TrimSpace(savedModel) != "" {
			m.modelName = savedModel
		}
		if messages, nodeID, err := m.sessions.LoadLatestMessages(m.sessionID); err == nil {
			m.history = append([]openrouter.Message{}, messages...)
			m.currentNodeID = nodeID
			m.chatBlocks = nil
			m.lines = nil
			m.appendIntroBlocks(m.sessionID, m.currentNodeID)
			m.appendHistoryBlocks(messages)
		} else {
			m.appendBlock(chatBlock{kind: bError, content: "failed to load initial session: " + err.Error()})
		}
	}

	return m
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{textinput.Blink}
	if m.runtime != nil {
		cmds = append(cmds,
			fireEventCmd(m.runtime, luaext.Event{Kind: luaext.EventSessionStart}),
			waitForStatus(m.statusCh),
		)
	}
	if m.permService != nil {
		cmds = append(cmds, waitForPermission(m.permService.Subscribe()))
	}
	return tea.Batch(cmds...)
}

// ── Update ─────────────────────────────────────────────────────────────────────

// Update is the Bubble Tea entrypoint. It routes mouse-wheel events directly
// to the viewport, then after every other message syncs viewport content and
// recalculates dimensions so inner handlers never touch the viewport.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Mouse-wheel → viewport scroll only in chat mode.
	if mouse, ok := msg.(tea.MouseMsg); ok && m.mode == chatMode {
		if mouse.Button == tea.MouseButtonWheelUp || mouse.Button == tea.MouseButtonWheelDown {
			var vpCmd tea.Cmd
			m.vp, vpCmd = m.vp.Update(mouse)
			switch mouse.Button {
			case tea.MouseButtonWheelUp:
				m.autoScroll = false // user scrolled up — stop chasing the bottom
			case tea.MouseButtonWheelDown:
				if m.vp.AtBottom() {
					m.autoScroll = true // user scrolled all the way back down
				}
			}
			return m.recalcViewport(), vpCmd
		}
	}

	next, cmd := m.doUpdate(msg)
	nm := next.(Model)
	nm.vp.SetContent(strings.Join(nm.lines, "\n"))
	nm = nm.recalcViewport()
	return nm, cmd
}

func (m Model) doUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Status updates from Lua extensions — handled in any mode.
	if upd, ok := msg.(statusUpdateMsg); ok {
		if upd.Text == "" {
			delete(m.statuses, upd.ID)
		} else {
			m.statuses[upd.ID] = upd.Text
		}
		return m, waitForStatus(m.statusCh)
	}

	// Always handle window resizes regardless of mode.
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		m.width = ws.Width
		m.height = ws.Height
		m.input.Width = max(20, ws.Width-4)
		// Rebuild glamour renderer with the new word-wrap width.
		// Use WithStandardStyle (not WithAutoStyle) to avoid an OSC 11 query.
		// Content area = termWidth - msgBlockPrefix (border+padding).
		// Glamour should wrap at that width so markdown fits cleanly inside blocks.
		wrapWidth := max(40, m.width-msgBlockPrefix)
		if r, err := glamour.NewTermRenderer(
			glamour.WithStandardStyle(m.mdStyle),
			glamour.WithWordWrap(wrapWidth),
		); err == nil {
			m.mdRenderer = r
		}
		// Re-render every block so widths are correct after resize.
		m.rebuildLines()
		if m.mode == pickerMode {
			var cmd tea.Cmd
			m.picker, cmd = m.picker.Update(ws)
			return m, cmd
		}
		if m.mode == resumeMode {
			var cmd tea.Cmd
			m.sessionPicker, cmd = m.sessionPicker.Update(ws)
			return m, cmd
		}
		if m.mode == nodePickMode {
			var cmd tea.Cmd
			m.nodePicker, cmd = m.nodePicker.Update(ws)
			return m, cmd
		}
		return m, nil
	}

	if m.mode == pickerMode {
		return m.updatePicker(msg)
	}
	if m.mode == resumeMode {
		return m.updateResumePicker(msg)
	}
	if m.mode == nodePickMode {
		return m.updateNodePicker(msg)
	}
	if m.mode == permissionMode {
		return m.updatePermission(msg)
	}
	return m.updateChat(msg)
}

func (m Model) updatePicker(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case modelPickedMsg:
		m.modelName = msg.modelID
		m.mode = chatMode
		m.input.Focus()
		if m.sessions != nil && m.sessionID != "" {
			if err := m.sessions.SetSessionModel(m.sessionID, m.modelName); err != nil {
				m.appendBlock(chatBlock{kind: bError, content: "failed to persist model: " + err.Error()})
			}
		}
		m.appendBlock(chatBlock{kind: bMeta, content: "Model set to " + m.modelName})
		return m, textinput.Blink
	case modelPickCanceledMsg:
		m.mode = chatMode
		m.input.Focus()
		return m, textinput.Blink
	default:
		var cmd tea.Cmd
		m.picker, cmd = m.picker.Update(msg)
		return m, cmd
	}
}

func (m Model) updateResumePicker(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case sessionPickedMsg:
		m.mode = chatMode
		m.input.Focus()
		if m.sessions == nil {
			m.appendBlock(chatBlock{kind: bError, content: "session store not available"})
			return m, textinput.Blink
		}
		messages, nodeID, err := m.sessions.LoadLatestMessages(msg.sessionID)
		if err != nil {
			m.appendBlock(chatBlock{kind: bError, content: "failed to load session: " + err.Error()})
			return m, textinput.Blink
		}
		m.sessionID = msg.sessionID
		m.currentNodeID = nodeID
		m.history = append([]openrouter.Message{}, messages...)
		if savedModel, err := m.sessions.GetSessionModel(m.sessionID); err == nil && strings.TrimSpace(savedModel) != "" {
			m.modelName = savedModel
		}
		m.chatBlocks = nil
		m.lines = nil
		m.appendIntroBlocks(m.sessionID, m.currentNodeID)
		m.appendHistoryBlocks(messages)
		m.appendBlock(chatBlock{kind: bMeta, content: fmt.Sprintf(
			"Resumed session %s at node %s (%d messages)",
			m.sessionID, shortID(m.currentNodeID), len(messages),
		)})
		m.appendBlock(chatBlock{kind: bMeta, content: "Model: " + m.modelName})
		return m, textinput.Blink

	case sessionPickCanceledMsg:
		m.mode = chatMode
		m.input.Focus()
		return m, textinput.Blink

	default:
		var cmd tea.Cmd
		m.sessionPicker, cmd = m.sessionPicker.Update(msg)
		return m, cmd
	}
}

func (m Model) updateNodePicker(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case nodePickedMsg:
		m.mode = chatMode
		m.input.Focus()
		if m.sessions == nil {
			m.appendBlock(chatBlock{kind: bError, content: "session store not available"})
			return m, textinput.Blink
		}
		messages, err := m.sessions.LoadMessagesAtNode(m.sessionID, msg.nodeID)
		if err != nil {
			m.appendBlock(chatBlock{kind: bError, content: "failed to load messages at node: " + err.Error()})
			return m, textinput.Blink
		}
		m.currentNodeID = msg.nodeID
		m.history = append([]openrouter.Message{}, messages...)
		m.chatBlocks = nil
		m.lines = nil
		m.appendIntroBlocks(m.sessionID, m.currentNodeID)
		m.appendHistoryBlocks(messages)
		m.appendBlock(chatBlock{kind: bMeta, content: fmt.Sprintf(
			"Checked out node %s (%d messages)",
			shortID(m.currentNodeID), len(messages),
		)})
		return m, textinput.Blink

	case nodePickCanceledMsg:
		m.mode = chatMode
		m.input.Focus()
		return m, textinput.Blink

	default:
		var cmd tea.Cmd
		m.nodePicker, cmd = m.nodePicker.Update(msg)
		return m, cmd
	}
}

func (m Model) updateChat(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// ── suggestion navigation ──────────────────────────────────────────
		if len(m.suggestions) > 0 {
			switch msg.String() {
			case "up", "ctrl+p":
				if m.suggCursor > 0 {
					m.suggCursor--
				}
				return m, nil
			case "down", "ctrl+n":
				if m.suggCursor < len(m.suggestions)-1 {
					m.suggCursor++
				}
				return m, nil
			case "tab", "enter":
				m = m.applySuggestion()
				return m, nil
			case "esc":
				m.suggestions = nil
				m.suggCursor = 0
				return m, nil
			}
		}

		// ── normal chat keys ───────────────────────────────────────────────
		key := msg.String()
		switch {
		case key == m.keybindings.ClearInput:
			// Clear the input field if it has content; otherwise no-op.
			if m.input.Value() != "" {
				m.input.SetValue("")
				m.suggestions = nil
				m.suggCursor = 0
			}
			return m, nil
		case key == m.keybindings.Quit:
			if m.runtime != nil {
				m.runtime.Fire(luaext.Event{Kind: luaext.EventSessionShutdown}) //nolint
			}
			m.pruneCurrentSessionIfEmpty()
			return m, tea.Quit
		case key == m.keybindings.CancelTurn:
			if m.running && m.cancelTurn != nil {
				m.cancelTurn()
				m.cancelTurn = nil
			}
			return m, nil
		case key == m.keybindings.ToggleThinking:
			m.toggleThinking()
		case key == m.keybindings.ToggleTools:
			m.toggleToolResults()
			return m, nil
		}
		switch key {
		case "tab":
			// Tab in shell (!) mode: trigger path autocompletion.
			if !m.running && strings.HasPrefix(m.input.Value(), "!") {
				m = m.triggerShellCompletion()
				return m, nil
			}
		case "enter":
			if m.running {
				return m, nil
			}
			value := strings.TrimSpace(m.input.Value())
			if value == "" {
				return m, nil
			}
			m.input.SetValue("")
			m.suggestions = nil
			m.suggCursor = 0
			m.shellCompletionBase = ""

			if strings.HasPrefix(value, "/") {
				return m.handleCommand(value)
			}
			if strings.HasPrefix(value, "!") {
				return m.submitShell(strings.TrimSpace(value[1:]))
			}
			return m.submitPrompt(value)
		}
	case spinner.TickMsg:
		if m.running {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
		return m, nil
	case thinkingTokenMsg:
		if m.streamingThinkingIdx < 0 {
			m.appendBlock(chatBlock{kind: bSep})
			m.streamingThinkingIdx = m.appendBlock(chatBlock{kind: bThinking, streaming: true})
		}
		m.chatBlocks[m.streamingThinkingIdx].content += msg.token
		m.updateBlock(m.streamingThinkingIdx)
		return m, waitForStream(m.streamCh)

	case tokenMsg:
		if m.streamingAssistantIdx < 0 {
			m.appendBlock(chatBlock{kind: bSep})
			m.streamingAssistantIdx = m.appendBlock(chatBlock{kind: bAssistant, streaming: true})
		}
		m.chatBlocks[m.streamingAssistantIdx].content += msg.token
		m.updateBlock(m.streamingAssistantIdx)
		return m, waitForStream(m.streamCh)

	case toolCallMsg:
		m.collapseThinkingBlock()
		m.streamingAssistantIdx = -1
		m.appendBlock(chatBlock{kind: bSep})
		// streaming=true means "still running"; flipped to false in toolResultMsg.
		m.appendBlock(chatBlock{kind: bToolCall, toolName: msg.name, toolArgs: msg.args, streaming: true})
		return m, waitForStream(m.streamCh)

	case toolResultMsg:
		content := msg.result
		if msg.err != nil {
			content = msg.err.Error()
		}
		// Update the matching bToolCall header so the icon flips to ✓/✗.
		for i := len(m.chatBlocks) - 1; i >= 0; i-- {
			if m.chatBlocks[i].kind == bToolCall && m.chatBlocks[i].toolName == msg.name {
				m.chatBlocks[i].streaming = false
				m.chatBlocks[i].isErr = msg.err != nil
				m.updateBlock(i)
				break
			}
		}
		m.appendBlock(chatBlock{
			kind:      bToolResult,
			toolName:  msg.name,
			content:   content,
			isErr:     msg.err != nil,
			collapsed: m.toolResultsCollapsed,
		})
		return m, waitForStream(m.streamCh)

	case turnDoneMsg:
		m.running = false
		if m.cancelTurn != nil {
			m.cancelTurn()
			m.cancelTurn = nil
		}
		m.collapseThinkingBlock()
		// Finalize the streaming assistant block with glamour-rendered markdown.
		if m.streamingAssistantIdx >= 0 {
			final := lastAssistantContent(msg.newMessages)
			if strings.TrimSpace(final) != "" {
				m.chatBlocks[m.streamingAssistantIdx].content = final
			}
			m.chatBlocks[m.streamingAssistantIdx].streaming = false
			m.updateBlock(m.streamingAssistantIdx)
		}
		m.streamingAssistantIdx = -1
		if len(msg.newMessages) > 0 {
			m.history = append(m.history, msg.newMessages...)
			if m.sessions != nil && m.sessionID != "" {
				nodeID, err := m.sessions.AppendMessages(m.sessionID, m.currentNodeID, msg.newMessages)
				if err != nil {
					m.appendBlock(chatBlock{kind: bError, content: "session write failed: " + err.Error()})
				} else {
					m.currentNodeID = nodeID
				}
			}
		}
		if m.runtime != nil {
			return m, fireEventCmd(m.runtime, luaext.Event{Kind: luaext.EventTurnEnd})
		}
		return m, nil

	case turnErrMsg:
		m.running = false
		m.collapseThinkingBlock()
		m.streamingAssistantIdx = -1
		if m.cancelTurn != nil {
			m.cancelTurn()
			m.cancelTurn = nil
		}
		if errors.Is(msg.err, context.Canceled) {
			m.appendBlock(chatBlock{kind: bError, content: "Cancelled."})
		} else {
			m.appendBlock(chatBlock{kind: bError, content: "Error: " + msg.err.Error()})
		}
		return m, nil

	case extCommandDoneMsg:
		if msg.err != nil {
			m.appendBlock(chatBlock{kind: bError, content: "command error: " + msg.err.Error()})
		}
		return m, nil

	case shellOutputMsg:
		if m.shellBlockIdx >= 0 {
			m.chatBlocks[m.shellBlockIdx].content += msg.text
			m.updateBlock(m.shellBlockIdx)
		}
		return m, waitForStream(m.shellCh)

	case shellDoneMsg:
		m.running = false
		if m.shellBlockIdx >= 0 {
			block := m.chatBlocks[m.shellBlockIdx]
			m.chatBlocks[m.shellBlockIdx].streaming = false
			if msg.err != nil {
				m.chatBlocks[m.shellBlockIdx].isErr = true
			}
			m.updateBlock(m.shellBlockIdx)
			// Persist the shell command + its full output as a single "cmd" node.
			if m.sessions != nil && m.sessionID != "" {
				content := "!" + block.command
				if strings.TrimSpace(block.content) != "" {
					content += "\n" + block.content
				}
				cmdMsg := openrouter.Message{Role: "cmd", Content: content}
				if newNodeID, err := m.sessions.AppendMessages(m.sessionID, m.shellCmdParentNodeID, []openrouter.Message{cmdMsg}); err == nil {
					m.currentNodeID = newNodeID
				}
			}
			m.shellBlockIdx = -1
		} else if msg.err != nil {
			m.appendBlock(chatBlock{kind: bError, content: "! " + msg.err.Error()})
		}
		return m, nil

	case permRequestMsg:
		m.currentPerm = &msg.req
		m.mode = permissionMode
		m.input.Blur()
		return m, nil
	}

	prev := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if m.input.Value() != prev {
		m = m.updateSuggestions()
	}
	return m, cmd
}

func (m Model) updateSuggestions() Model {
	val := m.input.Value()

	// Colour the prompt and text based on the leading character so the user
	// always knows which mode they're in:
	//   !  yellow — shell passthrough
	//   /  cyan   — slash command
	//   default   — normal (no override)
	switch {
	case strings.HasPrefix(val, "!"):
		m.input.PromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)
		m.input.TextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	case strings.HasPrefix(val, "/"):
		m.input.PromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
		m.input.TextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	default:
		m.input.PromptStyle = lipgloss.NewStyle()
		m.input.TextStyle = lipgloss.NewStyle()
	}

	// Show suggestions only when typing a command name (starts with / but no space yet).
	if strings.HasPrefix(val, "/") && !strings.Contains(val, " ") {
		m.suggestions = filterCommands(val, m.resourceCmds)
		if m.suggCursor >= len(m.suggestions) {
			m.suggCursor = max(0, len(m.suggestions)-1)
		}
		m.shellCompletionBase = ""
	} else {
		m.suggestions = nil
		m.suggCursor = 0
		m.shellCompletionBase = ""
	}
	return m
}

func (m Model) applySuggestion() Model {
	if len(m.suggestions) == 0 || m.suggCursor >= len(m.suggestions) {
		return m
	}
	// Path completion for shell (!) mode.
	if m.shellCompletionBase != "" {
		return m.applyPathSuggestion()
	}
	// Command completion.
	chosen := m.suggestions[m.suggCursor]
	if chosen.args != "" {
		m.input.SetValue(chosen.name + " ")
	} else {
		m.input.SetValue(chosen.name)
	}
	m.input.CursorEnd()
	m.suggestions = nil
	m.suggCursor = 0
	return m
}

// applyPathSuggestion applies the currently selected path suggestion in shell (!) mode.
func (m Model) applyPathSuggestion() Model {
	if len(m.suggestions) == 0 || m.suggCursor >= len(m.suggestions) {
		return m
	}
	chosen := m.suggestions[m.suggCursor]
	val := m.input.Value()
	pos := m.input.Position()
	textAfter := val[pos:]

	newVal := m.shellCompletionBase + chosen.name + textAfter
	m.input.SetValue(newVal)
	m.input.CursorEnd()

	// If the chosen path is a directory, re-trigger completion so the user
	// can keep navigating into sub-directories.
	if strings.HasSuffix(chosen.name, "/") {
		m.suggestions = nil
		m.suggCursor = 0
		return m.triggerShellCompletion()
	}
	m.shellCompletionBase = ""
	m.suggestions = nil
	m.suggCursor = 0
	return m
}

// triggerShellCompletion extracts the path prefix from the current shell (!)
// input and populates m.suggestions with file/directory completions.
func (m Model) triggerShellCompletion() Model {
	val := m.input.Value()
	if !strings.HasPrefix(val, "!") {
		return m
	}
	pos := m.input.Position()
	textBefore := val[:pos]
	shellText := textBefore[1:] // strip leading '!'

	// Find the start of the last whitespace-delimited token.
	lastSep := strings.LastIndexAny(shellText, " \t")
	var pathPrefix, base string
	if lastSep >= 0 {
		pathPrefix = shellText[lastSep+1:]
		base = "!" + shellText[:lastSep+1]
	} else {
		pathPrefix = shellText
		base = "!"
	}

	completions := getPathCompletions(pathPrefix)
	if len(completions) == 0 {
		return m
	}

	// Single match: apply it directly without showing the dropdown.
	if len(completions) == 1 {
		textAfter := val[pos:]
		m.input.SetValue(base + completions[0].name + textAfter)
		m.input.CursorEnd()
		// Re-enter sub-directory automatically.
		if strings.HasSuffix(completions[0].name, "/") {
			m.shellCompletionBase = ""
			return m.triggerShellCompletion()
		}
		m.shellCompletionBase = ""
		return m
	}

	m.shellCompletionBase = base
	m.suggestions = completions
	m.suggCursor = 0
	return m
}

func (m Model) submitPrompt(value string) (tea.Model, tea.Cmd) {
	if m.agent == nil {
		m.appendBlock(chatBlock{kind: bError, content: "Error: agent not initialized"})
		return m, nil
	}

	m.appendBlock(chatBlock{kind: bSep})
	m.appendBlock(chatBlock{kind: bUser, content: value})
	m.streamingAssistantIdx = -1
	m.streamingThinkingIdx = -1
	m.running = true
	m.streamCh = make(chan tea.Msg, 128)
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelTurn = cancel
	// Build LLM history, excluding "cmd" nodes (shell commands recorded for
	// display purposes only — they are not part of the model conversation).
	history := make([]openrouter.Message, 0, len(m.history))
	for _, msg := range m.history {
		if msg.Role != "cmd" {
			history = append(history, msg)
		}
	}
	// Prepend system prompt as first message each turn (not persisted to session).
	if sp := strings.TrimSpace(m.systemPrompt); sp != "" {
		history = append([]openrouter.Message{{Role: "system", Content: sp}}, history...)
	}
	rt := m.runtime // capture pointer; safe to call from agent goroutine

	go func(ctx context.Context, ch chan<- tea.Msg, input, modelName string, hist []openrouter.Message) {
		// Inject session ID so tools can associate permission requests with
		// the correct conversation.
		if m.sessionID != "" {
			ctx = permission.ContextWithSessionID(ctx, m.sessionID)
		}
		newMessages, err := m.agent.RunTurn(ctx, modelName, hist, input, agent.TurnCallbacks{
			OnToken: func(token string) {
				ch <- tokenMsg{token: token}
			},
			OnThinkingToken: func(token string) {
				ch <- thinkingTokenMsg{token: token}
			},
			// BeforeToolCall fires EventToolCall synchronously in the agent goroutine
			// so Lua handlers can block execution before it happens.
			BeforeToolCall: func(name, args string) bool {
				if rt == nil {
					return false
				}
				result, _ := rt.Fire(luaext.Event{
					Kind: luaext.EventToolCall,
					Data: map[string]any{"name": name, "args": args},
				})
				return result.Block
			},
			OnToolEvent: func(evt agent.ToolEvent) {
				switch evt.Kind {
				case "tool_call":
					ch <- toolCallMsg{name: evt.ToolName, args: evt.Arguments}
				case "tool_result":
					// Fire EventToolResult for observation/modification by extensions.
					if rt != nil {
						rt.Fire(luaext.Event{ //nolint — errors non-fatal
							Kind: luaext.EventToolResult,
							Data: map[string]any{"name": evt.ToolName, "result": evt.Result},
						})
					}
					ch <- toolResultMsg{name: evt.ToolName, result: evt.Result, err: evt.Err}
				}
			},
		})
		if err != nil {
			ch <- turnErrMsg{err: err}
			close(ch)
			return
		}
		ch <- turnDoneMsg{newMessages: newMessages}
		close(ch)
	}(ctx, m.streamCh, value, m.modelName, history)

	return m, tea.Batch(waitForStream(m.streamCh), m.spinner.Tick)
}

func (m Model) handleCommand(raw string) (tea.Model, tea.Cmd) {
	parts := strings.Fields(raw)
	if len(parts) == 0 {
		return m, nil
	}
	switch parts[0] {
	case "/quit":
		if m.runtime != nil {
			m.runtime.Fire(luaext.Event{Kind: luaext.EventSessionShutdown}) //nolint
		}
		m.pruneCurrentSessionIfEmpty()
		return m, tea.Quit

	case "/model":
		if len(parts) >= 2 {
			// Direct set by id — skip picker.
			id := parts[1]
			m.modelName = id
			if m.sessions != nil && m.sessionID != "" {
				if err := m.sessions.SetSessionModel(m.sessionID, m.modelName); err != nil {
					m.appendBlock(chatBlock{kind: bError, content: "failed to persist model: "+err.Error()})
				}
			}
			m.appendBlock(chatBlock{kind: bMeta, content: "Model set to "+m.modelName})
			return m, nil
		}
		// Open interactive picker.
		if m.catalog == nil {
			m.appendBlock(chatBlock{kind: bError, content: "model catalog not available"})
			return m, nil
		}
		m.mode = pickerMode
		m.input.Blur()
		pickerH := m.height
		if pickerH == 0 {
			pickerH = 40
		}
		pickerW := m.width
		if pickerW == 0 {
			pickerW = 120
		}
		m.picker = newPicker(pickerW, pickerH)
		return m, tea.Batch(fetchModels(m.catalog), textinput.Blink)

	case "/new":
		if m.sessions == nil {
			m.appendBlock(chatBlock{kind: bError, content: "Session store not available"})
			return m, nil
		}
		sessionID, err := m.sessions.NewSession()
		if err != nil {
			m.appendBlock(chatBlock{kind: bError, content: "Failed to create session: "+err.Error()})
			return m, nil
		}
		m.sessionID = sessionID
		m.currentNodeID = ""
		m.history = nil
		if m.sessions != nil && m.modelName != "" {
			if err := m.sessions.SetSessionModel(m.sessionID, m.modelName); err != nil {
				m.appendBlock(chatBlock{kind: bError, content: "Failed to persist session model: "+err.Error()})
			}
		}
		m.chatBlocks = nil
		m.lines = nil
		m.appendIntroBlocks(m.sessionID, m.currentNodeID)
		m.appendBlock(chatBlock{kind: bMeta, content: "Started new session: " + m.sessionID})
		return m, nil

	case "/sessions":
		if m.sessions == nil {
			m.appendBlock(chatBlock{kind: bError, content: "Session store not available"})
			return m, nil
		}
		// Always open interactive picker.
		m.mode = resumeMode
		m.input.Blur()
		w, h := m.width, m.height
		if w == 0 {
			w = 120
		}
		if h == 0 {
			h = 40
		}
		m.sessionPicker = newSessionPicker(w, h)
		return m, tea.Batch(fetchSessions(m.sessions), textinput.Blink)

	case "/label":
		if m.sessions == nil {
			m.appendBlock(chatBlock{kind: bError, content: "Session store not available"})
			return m, nil
		}
		if m.sessionID == "" {
			m.appendBlock(chatBlock{kind: bError, content: "No active session"})
			return m, nil
		}
		label := strings.Join(parts[1:], " ")
		if label == "" {
			// Show current label.
			current, err := m.sessions.GetSessionLabel(m.sessionID)
			if err != nil {
				m.appendBlock(chatBlock{kind: bError, content: "Failed to read label: "+err.Error()})
				return m, nil
			}
			if current == "" {
				m.appendBlock(chatBlock{kind: bMeta, content: "No label set. Use /label <text> to set one."})
			} else {
				m.appendBlock(chatBlock{kind: bMeta, content: "Label: "+current})
			}
			return m, nil
		}
		if err := m.sessions.SetSessionLabel(m.sessionID, label); err != nil {
			m.appendBlock(chatBlock{kind: bError, content: "Failed to set label: "+err.Error()})
			return m, nil
		}
		m.appendBlock(chatBlock{kind: bMeta, content: "Session labelled: "+label})
		return m, nil

	case "/system":
		text := strings.Join(parts[1:], " ")
		if text == "" {
			if m.systemPrompt == "" {
				m.appendBlock(chatBlock{kind: bMeta, content: "No system prompt set. Use /system <text> to set one."})
			} else {
				m.appendBlock(chatBlock{kind: bMeta, content: "System prompt: "+m.systemPrompt})
			}
			return m, nil
		}
		m.systemPrompt = strings.TrimSpace(text)
		m.appendBlock(chatBlock{kind: bMeta, content: "System prompt updated."})
		return m, nil

	case "/keybinds":
		kb := m.keybindings
		entries := []struct{ key, action string }{
			{kb.ClearInput, "clear input field"},
			{kb.Quit, "quit pigeon"},
			{kb.CancelTurn, "cancel running assistant turn"},
			{kb.ToggleThinking, "toggle thinking blocks"},
			{kb.ToggleTools, "toggle tool result blocks"},
		}
		// measure longest key for alignment
		maxLen := 0
		for _, e := range entries {
			if len(e.key) > maxLen {
				maxLen = len(e.key)
			}
		}
		keyStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))
		m.appendBlock(chatBlock{kind: bMeta, content: "Keybindings:"})
		for _, e := range entries {
			pad := strings.Repeat(" ", maxLen-len(e.key))
			m.appendBlock(chatBlock{kind: bMeta, content: "  " + keyStyle.Render(e.key) + pad + "  " + e.action})
		}
		return m, nil

	case "/tree":
		if m.sessions == nil {
			m.appendBlock(chatBlock{kind: bError, content: "Session store not available"})
			return m, nil
		}
		if m.sessionID == "" {
			m.appendBlock(chatBlock{kind: bMeta, content: "No active session. Use /resume first."})
			return m, nil
		}
		nodes, err := m.sessions.ListNodes(m.sessionID)
		if err != nil {
			m.appendBlock(chatBlock{kind: bError, content: "Failed to load tree: "+err.Error()})
			return m, nil
		}
		if len(nodes) == 0 {
			m.appendBlock(chatBlock{kind: bMeta, content: "Session tree is empty"})
			return m, nil
		}
		w, h := m.width, m.height
		if w == 0 {
			w = 120
		}
		if h == 0 {
			h = 40
		}
		m.mode = nodePickMode
		m.input.Blur()
		m.nodePicker = newNodePicker(nodes, m.currentNodeID, w, h)
		return m, nil

	default:
		cmd := parts[0]

		// /skill:<name>  — inject skill as a system message into history
		if strings.HasPrefix(cmd, "/skill:") {
			skillName := strings.TrimPrefix(cmd, "/skill:")
			if m.registry == nil {
				m.appendBlock(chatBlock{kind: bError, content: "no resource registry loaded"})
				return m, nil
			}
			skill, ok := m.registry.GetSkill(skillName)
			if !ok {
				m.appendBlock(chatBlock{kind: bError, content: "skill not found: "+skillName})
				return m, nil
			}
			sysMsg := openrouter.Message{Role: "system", Content: skill.Content}
			m.history = append(m.history, sysMsg)
			if m.sessions != nil && m.sessionID != "" {
				if _, err := m.sessions.AppendMessages(m.sessionID, m.currentNodeID, []openrouter.Message{sysMsg}); err != nil {
					m.appendBlock(chatBlock{kind: bError, content: "session write failed: "+err.Error()})
				}
			}
			m.appendBlock(chatBlock{kind: bMeta, content: "skill loaded: "+skillName})
			return m, nil
		}

		// /<promptname>  — expand prompt template into the input field
		if m.registry != nil {
			promptName := strings.TrimPrefix(cmd, "/")
			if prompt, ok := m.registry.GetPrompt(promptName); ok {
				m.input.SetValue(prompt.Content)
				m.input.CursorEnd()
				return m, nil
			}
		}

		// /<extcmd>  — extension-registered slash command
		if m.runtime != nil {
			extCmdName := strings.TrimPrefix(cmd, "/")
			if m.runtime.HasCommand(extCmdName) {
				args := strings.Join(parts[1:], " ")
				return m, runExtCommandCmd(m.runtime, extCmdName, args)
			}
		}

		m.appendBlock(chatBlock{kind: bError, content: "unknown command: "+cmd})
		return m, nil
	}
}

// ── View ───────────────────────────────────────────────────────────────────────

func (m Model) View() string {
	header := m.renderHeader()
	if m.mode == pickerMode {
		return lipgloss.JoinVertical(lipgloss.Left, header, "", m.picker.View())
	}
	if m.mode == resumeMode {
		return lipgloss.JoinVertical(lipgloss.Left, header, "", m.sessionPicker.View())
	}
	if m.mode == nodePickMode {
		return lipgloss.JoinVertical(lipgloss.Left, header, "", m.nodePicker.View())
	}
	return m.viewChat(header)
}

// viewChatWithPermDialog renders the chat view with the permission dialog
// replacing the normal input at the bottom.
func (m Model) viewChatWithPermDialog(header string) string {
	var statusLine string
	below := m.vp.TotalLineCount() - m.vp.YOffset - m.vp.Height
	if below > 0 {
		statusLine = metaStyle.Render(fmt.Sprintf("  ↓ %d more  ", below))
	}
	if m.running && statusLine == "" {
		statusLine = m.spinner.View()
	}
	if statusLine == "" {
		statusLine = " "
	}
	dialog := m.renderPermDialog()
	return lipgloss.JoinVertical(lipgloss.Left, header, "", m.vp.View(), statusLine, dialog)
}

func (m Model) renderHeader() string {
	status := "idle"
	if m.running {
		status = "streaming"
	}
	if m.mode == pickerMode {
		status = "picking model"
	}
	if m.mode == resumeMode {
		status = "picking session"
	}
	if m.mode == nodePickMode {
		status = "picking node"
	}
	if m.mode == permissionMode {
		status = "awaiting permission"
	}
	sessionText := "none"
	if m.sessionID != "" {
		sessionText = m.sessionID
	}
	nodeText := "root"
	if m.currentNodeID != "" {
		nodeText = shortID(m.currentNodeID)
	}
	return headerStyle.Render(fmt.Sprintf("pigeon • model=%s • session=%s • node=%s • %s", m.modelName, sessionText, nodeText, status))
}

func (m Model) viewChat(header string) string {
	if m.mode == permissionMode {
		return m.viewChatWithPermDialog(header)
	}
	// One reserved line between viewport and input: spinner while running,
	// scroll indicator when scrolled up, blank otherwise.
	var statusLine string
	if m.running {
		spinnerView := m.spinner.View()
		if !m.vp.AtBottom() {
			below := m.vp.TotalLineCount() - m.vp.YOffset - m.vp.Height
			if below > 0 {
				statusLine = metaStyle.Render(fmt.Sprintf("  ↓ %d more  ", below)) + spinnerView
			} else {
				statusLine = spinnerView
			}
		} else {
			statusLine = spinnerView
		}
	} else if !m.vp.AtBottom() {
		below := m.vp.TotalLineCount() - m.vp.YOffset - m.vp.Height
		if below > 0 {
			statusLine = metaStyle.Render(fmt.Sprintf("  ↓ %d more", below))
		}
	}
	if statusLine == "" {
		statusLine = " " // keep layout height stable
	}

	parts := []string{header, "", m.vp.View(), statusLine}
	if len(m.suggestions) > 0 {
		parts = append(parts, m.renderSuggestions())
	}
	parts = append(parts, m.input.View())
	if statusBar := m.renderStatusBar(); statusBar != "" {
		parts = append(parts, statusBar)
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func (m Model) renderStatusBar() string {
	if len(m.statuses) == 0 {
		return ""
	}
	// stable order: sort keys
	keys := make([]string, 0, len(m.statuses))
	for k := range m.statuses {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, m.statuses[k])
	}
	return metaStyle.Render(strings.Join(parts, "  ·  "))
}

func (m Model) renderSuggestions() string {
	// Limit visible suggestions to avoid overwhelming the input area.
	const maxVisible = 10
	suggestions := m.suggestions
	if len(suggestions) > maxVisible {
		suggestions = suggestions[:maxVisible]
	}

	var b strings.Builder
	for i, cmd := range suggestions {
		selected := i == m.suggCursor

		if selected {
			b.WriteString(suggSelectedStyle.Render("▶ " + cmd.name))
			if cmd.args != "" {
				b.WriteString(suggSelectedDimStyle.Render(" " + cmd.args))
			}
			if cmd.desc != "" {
				b.WriteString(suggSelectedDimStyle.Render("  — " + cmd.desc))
			}
		} else {
			label := cmd.name
			if cmd.args != "" {
				label += " " + suggDimStyle.Render(cmd.args)
			}
			if cmd.desc != "" {
				label += suggDimStyle.Render("  — " + cmd.desc)
			}
			b.WriteString("  " + label)
		}
		b.WriteString("\n")
	}
	if len(m.suggestions) > maxVisible {
		b.WriteString(suggDimStyle.Render(fmt.Sprintf("  … %d more", len(m.suggestions)-maxVisible)) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// ── helpers ────────────────────────────────────────────────────────────────────

// recalcViewport adjusts viewport dimensions to fit the current chrome (header,
// scroll indicator, suggestions, input, optional status bar) and scrolls to the
// bottom when autoScroll is true.
func (m Model) recalcViewport() Model {
	// Compute the actual number of terminal lines the header occupies.
	// headerStyle has no Width set, so lipgloss emits a single unsplit string
	// and the terminal wraps it.  We simulate that wrap here so the viewport
	// height is always accurate regardless of terminal width.
	headerLines := 1
	if m.width > 0 {
		headerLines = max(1, (lipgloss.Width(m.renderHeader())+m.width-1)/m.width)
	}

	var chrome int
	if m.mode == permissionMode {
		// header + blank(1) + scrollLine(1) + dialog
		chrome = headerLines + 1 + 1 + permDialogChrome - 1
	} else {
		// header + blank(1) + scrollLine(1) + input(1)
		visibleSuggs := len(m.suggestions)
		if visibleSuggs > 10 {
			visibleSuggs = 10 + 1 // +1 for the "… N more" line
		}
		chrome = headerLines + 3 + visibleSuggs
		if len(m.statuses) > 0 {
			chrome++ // status bar
		}
	}
	m.vp.Width = m.width
	m.vp.Height = max(3, m.height-chrome)
	if m.autoScroll {
		m.vp.GotoBottom()
	}
	return m
}

// pruneCurrentSessionIfEmpty deletes the active session when it has no user
// messages (i.e. the user quit without sending anything).
func (m *Model) pruneCurrentSessionIfEmpty() {
	if m.sessions == nil || m.sessionID == "" {
		return
	}
	first, err := m.sessions.GetFirstUserMessage(m.sessionID)
	if err != nil || first != "" {
		return
	}
	_ = m.sessions.DeleteSession(m.sessionID)
}

func waitForStream(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

// glamourStyle returns the glamour style name to use.
func glamourStyle() string {
	if s := strings.TrimSpace(os.Getenv("GLAMOUR_STYLE")); s != "" {
		return s
	}
	return "dark"
}

// ── chatBlock rendering ───────────────────────────────────────────────────────
//
// Crush-inspired approach: render the CONTENT at contentWidth (= termWidth - 2),
// then prepend a coloured border glyph to EVERY line of the output.
//
// This avoids lipgloss Width() semantics entirely for block sizing:
//   - no interaction between Width, Padding, and Border to reason about
//   - background fills exactly contentWidth columns per line
//   - border is always exactly 1 column, prepended to each line
//   - correct on any terminal width, including before WindowSizeMsg

// contentWidth returns the number of columns available for message content.
// msgBlockPrefix (2) = border glyph (1) + padding space (1).
func contentWidth(termWidth int) int {
	if termWidth <= msgBlockPrefix {
		return 20 // safe fallback before WindowSizeMsg arrives
	}
	return termWidth - msgBlockPrefix
}

// prefixEachLine prepends prefix to every line of s.
func prefixEachLine(prefix, s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

// applyBackground pads every line of content to width cols and applies bg.
// This gives each block a consistent rectangular background without using
// lipgloss Width() on the outer container.
func applyBackground(content string, bg lipgloss.Color, width int) string {
	bgStyle := lipgloss.NewStyle().Background(bg).Width(width)
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		lines[i] = bgStyle.Render(line)
	}
	return strings.Join(lines, "\n")
}

// stripBlankLines removes blank (whitespace-only) lines from s.
// Glamour adds blank lines between elements for readability; inside bordered
// blocks those blank lines, after applyBackground, produce full-width
// background-coloured rectangles that look like solid bars / black squares.
func stripBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	out := lines[:0]
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// renderBlock renders a single chatBlock to a string using the current terminal width.
func (m Model) renderBlock(b chatBlock) string {
	cw := contentWidth(m.width)
	switch b.kind {
	case bSep:
		return ""
	case bMeta:
		return metaStyle.Render(b.content)
	case bError:
		return errorStyle.Render(b.content)
	case bShell:
		return shellStyle.Render(b.content)
	case bShellBlock:
		// Shell command + output rendered exactly like a user message (same bg and border)
		// but with a monospace command header and dimmed output text.
		padLine := lipgloss.NewStyle().Background(bgShell).Width(cw).Render("")
		blankLine := lipgloss.NewStyle().Background(bgShell).PaddingLeft(2).Width(cw).Render("")

		// Status icon appended to the command header.
		var statusMark string
		switch {
		case b.streaming:
			statusMark = "  " + lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render("●")
		case b.isErr:
			statusMark = "  " + lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render("✗")
		default:
			statusMark = "  " + lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render("✓")
		}
		cmdStyle := lipgloss.NewStyle().
			Background(bgShell).PaddingLeft(2).Width(cw).
			Foreground(lipgloss.Color("15")).Bold(true)
		outputStyle := lipgloss.NewStyle().
			Background(bgShell).PaddingLeft(2).Width(cw).
			Foreground(lipgloss.Color("8"))

		var contentLines []string
		contentLines = append(contentLines, padLine)
		contentLines = append(contentLines, cmdStyle.Render("$ "+b.command+statusMark))
		if b.content != "" {
			contentLines = append(contentLines, blankLine) // space between cmd and output
			for _, line := range strings.Split(strings.TrimRight(b.content, "\n"), "\n") {
				contentLines = append(contentLines, outputStyle.Render(line))
			}
		}
		contentLines = append(contentLines, padLine)
		prefix := shellBlockBorderPrefix.Render()
		return prefixEachLine(prefix, strings.Join(contentLines, "\n"))
	case bUser:
		// Padding line: full-width background-coloured empty row for top/bottom spacing.
		padLine := lipgloss.NewStyle().Background(bgUser).Width(cw).Render("")
		// Content lines: left-padded inside the background so text breathes.
		// PaddingLeft(2) means text starts 2 cols in; Width(cw) keeps total width consistent.
		lineStyle := lipgloss.NewStyle().Background(bgUser).PaddingLeft(2).Width(cw)
		var contentLines []string
		contentLines = append(contentLines, padLine)
		for _, line := range strings.Split(b.content, "\n") {
			contentLines = append(contentLines, lineStyle.Render(line))
		}
		contentLines = append(contentLines, padLine)
		prefix := userBorderPrefix.Render()
		return prefixEachLine(prefix, strings.Join(contentLines, "\n"))
	case bAssistant:
		// No border, no background — plain glamour output flows directly into
		// the viewport so it reads like natural terminal text.
		if b.streaming {
			return b.content
		}
		return stripBlankLines(m.renderMarkdown(b.content, cw))
	case bThinking:
		var text string
		if b.streaming {
			text = thinkingStyle.Render("💭 " + b.content)
		} else if b.collapsed {
			text = metaStyle.Render("💭 [thinking]")
		} else {
			text = thinkingStyle.Render("💭 " + b.content)
		}
		return text
	case bToolCall:
		// Header line: icon (updates when result arrives) + name + args.
		var icon string
		switch {
		case b.streaming:
			icon = toolIconRunning.Render()
		case b.isErr:
			icon = toolIconError.Render()
		default:
			icon = toolIconSuccess.Render()
		}
		line := icon + " " + toolNameStyle.Render(b.toolName)
		if b.toolArgs != "" && b.toolArgs != "{}" {
			line += "  " + toolArgsStyle.Render(b.toolArgs)
		}
		return line

	case bToolResult:
		// Body only — the bToolCall above already shows the header.
		if b.content == "" {
			return ""
		}
		raw := strings.TrimRight(b.content, "\n")
		lines := strings.Split(raw, "\n")

		lineStyle := lipgloss.NewStyle().PaddingLeft(2)

		maxLines := len(lines)
		if b.collapsed {
			maxLines = toolResultPreviewLines
		}

		var out []string
		for i, l := range lines {
			if i >= maxLines {
				break
			}
			out = append(out, lineStyle.Render(l))
		}

		if b.collapsed && len(lines) > toolResultPreviewLines {
			out = append(out, toolTruncStyle.Render(fmt.Sprintf(
				"  … %d more lines  [alt+r to expand]", len(lines)-toolResultPreviewLines,
			)))
		} else if !b.collapsed && len(lines) > toolResultPreviewLines {
			out = append(out, toolTruncStyle.Render("  [alt+r to collapse]"))
		}
		return strings.Join(out, "\n")
	}
	return ""
}

// renderMarkdown runs content through glamour and trims the surrounding
// newlines that glamour always adds.
func (m Model) renderMarkdown(content string, width int) string {
	if m.mdRenderer == nil || width <= 0 {
		return content
	}
	rendered, err := m.mdRenderer.Render(content)
	if err != nil {
		return content
	}
	return strings.Trim(rendered, "\n")
}

// ── chatBlock management ──────────────────────────────────────────────────────

// appendBlock renders b, appends it to chatBlocks and lines, and returns its index.
func (m *Model) appendBlock(b chatBlock) int {
	idx := len(m.chatBlocks)
	m.chatBlocks = append(m.chatBlocks, b)
	m.lines = append(m.lines, m.renderBlock(b))
	return idx
}

// updateBlock re-renders chatBlocks[idx] and updates lines[idx] in-place.
func (m *Model) updateBlock(idx int) {
	if idx < 0 || idx >= len(m.chatBlocks) {
		return
	}
	m.lines[idx] = m.renderBlock(m.chatBlocks[idx])
}

// rebuildLines re-renders every block from scratch using the current width.
// Called after a terminal resize or glamour renderer change.
func (m *Model) rebuildLines() {
	for i := range m.chatBlocks {
		m.lines[i] = m.renderBlock(m.chatBlocks[i])
	}
}

// appendIntroBlocks populates the welcome banner blocks.
func (m *Model) appendIntroBlocks(sessionID, nodeID string) {
	m.appendBlock(chatBlock{kind: bMeta, content: "Welcome to pigeon."})
	m.appendBlock(chatBlock{kind: bMeta, content: "Type /quit to exit."})
	if sessionID != "" {
		m.appendBlock(chatBlock{kind: bMeta, content: "Session: " + sessionID})
	}
	if nodeID != "" {
		m.appendBlock(chatBlock{kind: bMeta, content: "Node: " + shortID(nodeID)})
	}
}

// appendHistoryBlocks converts loaded session messages into chatBlocks.
func (m *Model) appendHistoryBlocks(messages []openrouter.Message) {
	for _, msg := range messages {
		switch msg.Role {
		case "user":
			if strings.TrimSpace(msg.Content) != "" {
				m.appendBlock(chatBlock{kind: bSep})
				m.appendBlock(chatBlock{kind: bUser, content: msg.Content})
			}
		case "assistant":
			if strings.TrimSpace(msg.Content) != "" {
				m.appendBlock(chatBlock{kind: bSep})
				// streaming: false → renderBlock will use glamour
				m.appendBlock(chatBlock{kind: bAssistant, content: msg.Content})
			}
			for _, tc := range msg.ToolCalls {
				m.appendBlock(chatBlock{kind: bSep})
				// streaming=false: result already exists in history.
				m.appendBlock(chatBlock{kind: bToolCall, toolName: tc.Function.Name, toolArgs: tc.Function.Arguments, streaming: false})
			}
		case "tool":
			name := msg.Name
			if strings.TrimSpace(name) == "" {
				name = "tool"
			}
			m.appendBlock(chatBlock{kind: bSep})
			m.appendBlock(chatBlock{
				kind:      bToolResult,
				toolName:  name,
				content:   msg.Content,
				collapsed: m.toolResultsCollapsed,
			})
		case "cmd":
			// Content format: "!<command>\n<output>" — output may be absent.
			raw := msg.Content
			if !strings.HasPrefix(raw, "!") {
				break
			}
			raw = strings.TrimPrefix(raw, "!")
			command, output, _ := strings.Cut(raw, "\n")
			if command != "" {
				m.appendBlock(chatBlock{kind: bSep})
				m.appendBlock(chatBlock{kind: bShellBlock, command: command, content: output, streaming: false})
			}
		}
	}
}

// collapseThinkingBlock finalises the streaming thinking block.
func (m *Model) collapseThinkingBlock() {
	if m.streamingThinkingIdx < 0 {
		return
	}
	m.chatBlocks[m.streamingThinkingIdx].streaming = false
	m.chatBlocks[m.streamingThinkingIdx].collapsed = m.thinkingCollapsed
	m.updateBlock(m.streamingThinkingIdx)
	m.streamingThinkingIdx = -1
}

// toggleThinking flips thinkingCollapsed and re-renders every finished thinking block.
func (m *Model) toggleThinking() {
	m.thinkingCollapsed = !m.thinkingCollapsed
	for i := range m.chatBlocks {
		if m.chatBlocks[i].kind == bThinking && !m.chatBlocks[i].streaming {
			m.chatBlocks[i].collapsed = m.thinkingCollapsed
			m.updateBlock(i)
		}
	}
}

// toggleToolResults flips toolResultsCollapsed and re-renders every tool result block.
func (m *Model) toggleToolResults() {
	m.toolResultsCollapsed = !m.toolResultsCollapsed
	for i := range m.chatBlocks {
		if m.chatBlocks[i].kind == bToolResult {
			m.chatBlocks[i].collapsed = m.toolResultsCollapsed
			m.updateBlock(i)
		}
	}
}


func summarize(input string) string {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "(no output)"
	}
	line := strings.Split(trimmed, "\n")[0]
	if len(line) > 120 {
		return line[:120] + "..."
	}
	return line
}

func shortID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

func lastAssistantContent(messages []openrouter.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" && strings.TrimSpace(messages[i].Content) != "" {
			return messages[i].Content
		}
	}
	return ""
}



func renderTree(nodes []session.Node, currentNodeID string) []string {
	children := make(map[string][]session.Node)
	for _, node := range nodes {
		parent := strings.TrimSpace(node.ParentID)
		children[parent] = append(children[parent], node)
	}
	for parent := range children {
		sortByRecorded(children[parent])
	}
	roots := children[""]
	if len(roots) == 0 {
		return []string{"(no roots found)"}
	}
	if linear, ok := renderLinearTree(children, roots, currentNodeID); ok {
		return linear
	}
	var out []string
	var walk func(parent, prefix string)
	walk = func(parent, prefix string) {
		for idx, kid := range children[parent] {
			isLast := idx == len(children[parent])-1
			connector, nextPrefix := "├─", prefix+"│ "
			if isLast {
				connector, nextPrefix = "└─", prefix+"  "
			}
			marker := " "
			if kid.ID == currentNodeID {
				marker = "*"
			}
			out = append(out, fmt.Sprintf("%s%s%s %s [%s]", prefix, connector, marker, shortID(kid.ID), kid.Message.Role))
			walk(kid.ID, nextPrefix)
		}
	}
	walk("", "")
	return out
}

func renderLinearTree(children map[string][]session.Node, roots []session.Node, currentNodeID string) ([]string, bool) {
	if len(roots) != 1 {
		return nil, false
	}
	for _, kids := range children {
		if len(kids) > 1 {
			return nil, false
		}
	}
	path := make([]session.Node, 0)
	cursor := roots[0]
	for {
		path = append(path, cursor)
		next := children[cursor.ID]
		if len(next) == 0 {
			break
		}
		cursor = next[0]
	}
	out := make([]string, 0, len(path))
	for _, node := range path {
		marker := " "
		if node.ID == currentNodeID {
			marker = "*"
		}
		desc := summarize(node.Message.Content)
		if desc == "(no output)" {
			desc = ""
		}
		line := fmt.Sprintf("• %s %s [%s]", marker, shortID(node.ID), node.Message.Role)
		if desc != "" {
			line += ": " + desc
		}
		out = append(out, line)
	}
	return out, true
}

// ── permission dialog ─────────────────────────────────────────────────────────

// updatePermission handles keyboard input while a permission dialog is shown.
func (m Model) updatePermission(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		// Pass stream messages through so the spinner keeps ticking.
		if _, spin := msg.(spinner.TickMsg); spin && m.running {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
		return m, nil
	}

	if m.currentPerm == nil || m.permService == nil {
		m.mode = chatMode
		m.input.Focus()
		return m, textinput.Blink
	}

	id := m.currentPerm.ID

	switch key.String() {
	case "y", "Y", "a", "A", "enter":
		// Allow once.
		m.permService.Grant(id)
		return m.closePermDialog()

	case "s", "S":
		// Allow for session — cache this specific tool+action+path.
		m.permService.GrantPersistent(id)
		return m.closePermDialog()

	case "n", "N", "d", "D", "esc":
		// Deny.
		m.permService.Deny(id)
		m.appendBlock(chatBlock{kind: bError, content: fmt.Sprintf("⛔ Permission denied: %s %s", m.currentPerm.ToolName, m.currentPerm.Action)})
		return m.closePermDialog()
	}

	return m, nil
}

// closePermDialog clears the current permission request and returns to chat mode.
func (m Model) closePermDialog() (tea.Model, tea.Cmd) {
	m.currentPerm = nil
	m.mode = chatMode
	m.input.Focus()
	// Resume listening for the next permission request.
	var permCmd tea.Cmd
	if m.permService != nil {
		permCmd = waitForPermission(m.permService.Subscribe())
	}
	return m, tea.Batch(textinput.Blink, permCmd)
}

// renderPermDialog renders the permission request dialog box.
func (m Model) renderPermDialog() string {
	if m.currentPerm == nil {
		return ""
	}
	req := m.currentPerm
	dialogWidth := m.width - 4
	if dialogWidth < 40 {
		dialogWidth = 40
	}
	innerWidth := dialogWidth - 4 // account for border + padding

	var b strings.Builder

	// Title
	b.WriteString(permTitleStyle.Render("🔒 Permission Required") + "\n")

	// Tool + Action
	b.WriteString(
		permLabelStyle.Render("Tool   ") +
			permValueStyle.Render(req.ToolName) +
			permLabelStyle.Render("  ·  action  ") +
			permValueStyle.Render(req.Action) + "\n",
	)

	// Path
	b.WriteString(permLabelStyle.Render("Path   ") + permValueStyle.Render(req.Path) + "\n")

	// Tool-specific content block
	content := m.renderPermContent(req, innerWidth)
	if content != "" {
		b.WriteString("\n" + content + "\n")
	}

	// Buttons
	b.WriteString("\n")
	buttons := permAllowStyle.Render("[y] Allow") +
		"   " +
		permSessionStyle.Render("[s] Allow for Session") +
		"   " +
		permDenyStyle.Render("[n] Deny")
	b.WriteString(buttons + "\n")
	b.WriteString(permHelpStyle.Render("esc = deny"))

	box := permBorderStyle.Width(innerWidth).Render(b.String())
	return lipgloss.NewStyle().Width(m.width).Render(box)
}

// renderPermContent renders the tool-specific body of the permission dialog.
func (m Model) renderPermContent(req *permission.Request, width int) string {
	switch req.ToolName {
	case "bash":
		params, ok := req.Params.(permission.BashParams)
		if !ok {
			return ""
		}
		cmd := params.Command
		if len(cmd) > width {
			cmd = cmd[:width-3] + "..."
		}
		return permCodeStyle.Width(width).Render("$ " + cmd)

	case "write":
		params, ok := req.Params.(permission.WriteParams)
		if !ok {
			return ""
		}
		preview := params.Content
		lines := strings.Split(preview, "\n")
		if len(lines) > 8 {
			lines = append(lines[:8], fmt.Sprintf("… (%d more lines)", len(lines)-8))
		}
		var b strings.Builder
		for _, l := range lines {
			if len(l) > width-2 {
				l = l[:width-5] + "..."
			}
			b.WriteString(permDiffAddStyle.Render("+ "+l) + "\n")
		}
		return strings.TrimRight(b.String(), "\n")

	case "edit":
		params, ok := req.Params.(permission.EditParams)
		if !ok {
			return ""
		}
		var b strings.Builder
		// Old text (removals)
		oldLines := strings.Split(strings.TrimRight(params.OldText, "\n"), "\n")
		if len(oldLines) > 5 {
			oldLines = append(oldLines[:5], fmt.Sprintf("… (%d more lines)", len(oldLines)-5))
		}
		for _, l := range oldLines {
			if len(l) > width-2 {
				l = l[:width-5] + "..."
			}
			b.WriteString(permDiffDelStyle.Render("- "+l) + "\n")
		}
		// New text (additions)
		newLines := strings.Split(strings.TrimRight(params.NewText, "\n"), "\n")
		if len(newLines) > 5 {
			newLines = append(newLines[:5], fmt.Sprintf("… (%d more lines)", len(newLines)-5))
		}
		for _, l := range newLines {
			if len(l) > width-2 {
				l = l[:width-5] + "..."
			}
			b.WriteString(permDiffAddStyle.Render("+ "+l) + "\n")
		}
		return strings.TrimRight(b.String(), "\n")

	default:
		if req.Description != "" {
			desc := req.Description
			if len(desc) > width {
				desc = desc[:width-3] + "..."
			}
			return permCodeStyle.Width(width).Render(desc)
		}
		return ""
	}
}

// runShellCmd runs a shell command (from a ! prefix) and sends shellOutputMsg
// chunks and a final shellDoneMsg back to the update loop.
func runShellCmd(command string, ch chan<- tea.Msg) tea.Cmd {
	return func() tea.Msg {
		cmd := exec.Command("sh", "-c", command)
		var buf bytes.Buffer
		cmd.Stdout = &buf
		cmd.Stderr = &buf
		err := cmd.Run()
		output := buf.String()
		if output != "" {
			ch <- shellOutputMsg{text: output}
		}
		ch <- shellDoneMsg{err: err}
		return nil
	}
}

// waitForPermission returns a Cmd that reads one permission request from ch and
// wraps it in a permRequestMsg.
func waitForPermission(ch <-chan permission.Request) tea.Cmd {
	return func() tea.Msg {
		req, ok := <-ch
		if !ok {
			return nil
		}
		return permRequestMsg{req: req}
	}
}

// ── shell (!) passthrough ──────────────────────────────────────────────────────

// submitShell runs a raw shell command entered as !<command> in the chat input.
// The command and its output are displayed together in a bShellBlock.
func (m Model) submitShell(command string) (tea.Model, tea.Cmd) {
	if command == "" {
		return m, nil
	}

	// Capture the current node ID now; the cmd node will be written in shellDoneMsg
	// once the full output is available.
	m.shellCmdParentNodeID = m.currentNodeID

	m.appendBlock(chatBlock{kind: bSep})
	idx := m.appendBlock(chatBlock{kind: bShellBlock, command: command, streaming: true})
	m.shellBlockIdx = idx
	m.running = true
	m.shellCh = make(chan tea.Msg, 64)

	return m, tea.Batch(
		runShellCmd(command, m.shellCh),
		waitForStream(m.shellCh),
		m.spinner.Tick,
	)
}

// ── Lua runtime helpers ───────────────────────────────────────────────────────

func fireEventCmd(rt *luaext.Runtime, event luaext.Event) tea.Cmd {
	return func() tea.Msg {
		rt.Fire(event) //nolint — errors logged inside Fire
		return nil
	}
}

func waitForStatus(ch <-chan luaext.StatusUpdate) tea.Cmd {
	return func() tea.Msg {
		upd, ok := <-ch
		if !ok {
			return nil
		}
		return statusUpdateMsg(upd)
	}
}

func runExtCommandCmd(rt *luaext.Runtime, name, args string) tea.Cmd {
	return func() tea.Msg {
		return extCommandDoneMsg{err: rt.FireCommand(name, args)}
	}
}

func buildResourceCmds(reg *resources.Registry, rt *luaext.Runtime) []commandDef {
	var cmds []commandDef
	if reg != nil {
		for _, s := range reg.ListSkills() {
			cmds = append(cmds, commandDef{
				name: "/skill:" + s.Name,
				desc: "inject skill into context",
			})
		}
		for _, p := range reg.ListPrompts() {
			cmds = append(cmds, commandDef{
				name: "/" + p.Name,
				desc: "expand prompt template",
			})
		}
	}
	if rt != nil {
		for _, c := range rt.ListCommands() {
			cmds = append(cmds, commandDef{
				name: "/" + c.Name,
				desc: c.Desc,
			})
		}
	}
	return cmds
}

func sortByRecorded(nodes []session.Node) {
	for i := 0; i < len(nodes)-1; i++ {
		for j := i + 1; j < len(nodes); j++ {
			if nodes[j].RecordedAt.Before(nodes[i].RecordedAt) {
				nodes[i], nodes[j] = nodes[j], nodes[i]
			}
		}
	}
}

// ── shell path autocompletion ─────────────────────────────────────────────────

// getPathCompletions returns commandDef completions for the given path prefix.
// It reads the directory that the prefix points into and returns entries whose
// names begin with the token after the last '/'.
//
// Supported prefix forms:
//
//	""        → entries in the current working directory
//	"foo"     → entries in cwd starting with "foo"
//	"src/"    → all entries inside ./src/
//	"./sr"    → entries in cwd starting with "sr"
//	"~/doc"   → entries in $HOME starting with "doc"
//	"/usr/li" → entries in /usr/ starting with "li"
func getPathCompletions(prefix string) []commandDef {
	homeDir, _ := os.UserHomeDir()

	// ── determine lookDir and filePrefix ─────────────────────────────────
	var lookDir, filePrefix, displayDirPart string

	if strings.HasPrefix(prefix, "~/") {
		// Home-relative: expand ~ for lookup but keep ~/ in the displayed result.
		rest := prefix[2:]
		if idx := strings.LastIndex(rest, "/"); idx >= 0 {
			lookDir = filepath.Join(homeDir, rest[:idx+1])
			filePrefix = rest[idx+1:]
			displayDirPart = "~/" + rest[:idx+1]
		} else {
			lookDir = homeDir
			filePrefix = rest
			displayDirPart = "~/"
		}
	} else if strings.Contains(prefix, "/") {
		// Absolute or relative path with at least one slash.
		idx := strings.LastIndex(prefix, "/")
		lookDir = prefix[:idx+1]
		if lookDir == "" {
			lookDir = "/"
		}
		filePrefix = prefix[idx+1:]
		displayDirPart = prefix[:idx+1]
	} else {
		// Plain token — complete against current working directory.
		lookDir = "."
		filePrefix = prefix
		displayDirPart = ""
	}

	entries, err := os.ReadDir(lookDir)
	if err != nil {
		return nil
	}

	var completions []commandDef
	for _, entry := range entries {
		name := entry.Name()
		// Skip hidden files unless the user explicitly started typing a dot.
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(filePrefix, ".") {
			continue
		}
		if filePrefix != "" && !strings.HasPrefix(strings.ToLower(name), strings.ToLower(filePrefix)) {
			continue
		}
		result := displayDirPart + name
		if entry.IsDir() {
			result += "/"
		}
		completions = append(completions, commandDef{name: result})
	}
	return completions
}
