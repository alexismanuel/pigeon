package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"pigeon/internal/agent"
	luaext "pigeon/internal/extensions/lua"
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
	chatMode   appMode = iota
	pickerMode appMode = iota
	resumeMode appMode = iota
)

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
}

type modelCatalog interface {
	ListModels(ctx context.Context) ([]openrouter.ModelInfo, error)
}

// ── chat event messages ────────────────────────────────────────────────────────

type tokenMsg struct {
	token string
}

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

	sessionID     string
	currentNodeID string
	history       []openrouter.Message
	modelName     string
	systemPrompt  string // injected as first message each turn; "" = none

	input            textinput.Model
	vp               viewport.Model
	autoScroll       bool // scroll to bottom whenever new content arrives
	lines            []string
	streamCh         chan tea.Msg
	running          bool
	width            int
	height           int
	assistantLineIdx int

	registry     *resources.Registry
	resourceCmds []commandDef // dynamic commands built from registry + extension commands

	runtime  *luaext.Runtime
	statusCh <-chan luaext.StatusUpdate
	statuses map[string]string // id → text, from extension set_status calls

	suggestions []commandDef
	suggCursor  int
}

var (
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	userStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10"))
	asstStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))
	metaStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

func NewModel(ag turnRunner, catalog modelCatalog, modelName string, sessions sessionStore, sessionID string, reg *resources.Registry, rt *luaext.Runtime, statusCh <-chan luaext.StatusUpdate, systemPrompt ...string) Model {
	in := textinput.New()
	in.Placeholder = "Ask pigeon..."
	in.Prompt = "> "
	in.Focus()
	in.CharLimit = 0
	in.Width = 100

	vp := viewport.New(0, 0)
	vp.KeyMap = viewport.KeyMap{} // disable all keyboard bindings; mouse wheel only

	m := Model{
		agent:            ag,
		catalog:          catalog,
		sessions:         sessions,
		sessionID:        strings.TrimSpace(sessionID),
		modelName:        strings.TrimSpace(modelName),
		input:            in,
		vp:               vp,
		autoScroll:       true,
		lines:            introLines(strings.TrimSpace(sessionID), ""),
		assistantLineIdx: -1,
		registry:         reg,
		runtime:          rt,
		statusCh:         statusCh,
		statuses:         make(map[string]string),
		resourceCmds:     buildResourceCmds(reg, rt),
	}
	if len(systemPrompt) > 0 {
		m.systemPrompt = strings.TrimSpace(systemPrompt[0])
	}

	if m.sessions != nil && m.sessionID != "" {
		if savedModel, err := m.sessions.GetSessionModel(m.sessionID); err == nil && strings.TrimSpace(savedModel) != "" {
			m.modelName = savedModel
		}
		if messages, nodeID, err := m.sessions.LoadLatestMessages(m.sessionID); err == nil {
			m.history = append([]openrouter.Message{}, messages...)
			m.currentNodeID = nodeID
			m.lines = introLines(m.sessionID, m.currentNodeID)
			m.lines = append(m.lines, renderHistoryLines(messages)...)
		} else {
			m.lines = append(m.lines, errorStyle.Render("failed to load initial session: "+err.Error()))
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
		return m, nil
	}

	if m.mode == pickerMode {
		return m.updatePicker(msg)
	}
	if m.mode == resumeMode {
		return m.updateResumePicker(msg)
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
				m.lines = append(m.lines, errorStyle.Render("failed to persist model: "+err.Error()))
			}
		}
		m.lines = append(m.lines, metaStyle.Render("Model set to "+m.modelName))
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
			m.lines = append(m.lines, errorStyle.Render("session store not available"))
			return m, textinput.Blink
		}
		messages, nodeID, err := m.sessions.LoadLatestMessages(msg.sessionID)
		if err != nil {
			m.lines = append(m.lines, errorStyle.Render("failed to load session: "+err.Error()))
			return m, textinput.Blink
		}
		m.sessionID = msg.sessionID
		m.currentNodeID = nodeID
		m.history = append([]openrouter.Message{}, messages...)
		if savedModel, err := m.sessions.GetSessionModel(m.sessionID); err == nil && strings.TrimSpace(savedModel) != "" {
			m.modelName = savedModel
		}
		m.lines = introLines(m.sessionID, m.currentNodeID)
		m.lines = append(m.lines, renderHistoryLines(messages)...)
		m.lines = append(m.lines, metaStyle.Render(fmt.Sprintf(
			"Resumed session %s at node %s (%d messages)",
			m.sessionID, shortID(m.currentNodeID), len(messages),
		)))
		m.lines = append(m.lines, metaStyle.Render("Model: "+m.modelName))
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
		switch msg.String() {
		case "ctrl+c":
			if m.runtime != nil {
				m.runtime.Fire(luaext.Event{Kind: luaext.EventSessionShutdown}) //nolint
			}
			return m, tea.Quit
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

			if strings.HasPrefix(value, "/") {
				return m.handleCommand(value)
			}
			return m.submitPrompt(value)
		}
	case tokenMsg:
		if m.assistantLineIdx < 0 {
			// Start a new assistant line (happens after tool-call rounds).
			m.lines = append(m.lines, assistantLine(""))
			m.assistantLineIdx = len(m.lines) - 1
		}
		m.lines[m.assistantLineIdx] += msg.token
		return m, waitForStream(m.streamCh)
	case toolCallMsg:
		// Reset so the follow-up assistant message starts on its own line.
		m.assistantLineIdx = -1
		m.lines = append(m.lines, metaStyle.Render(fmt.Sprintf("↳ tool call: %s(%s)", msg.name, msg.args)))
		return m, waitForStream(m.streamCh)
	case toolResultMsg:
		if msg.err != nil {
			m.lines = append(m.lines, errorStyle.Render(fmt.Sprintf("↳ tool error (%s): %v", msg.name, msg.err)))
		} else {
			m.lines = append(m.lines, metaStyle.Render(fmt.Sprintf("↳ tool result (%s): %s", msg.name, summarize(msg.result))))
		}
		return m, waitForStream(m.streamCh)
	case turnDoneMsg:
		m.running = false
		if len(msg.newMessages) > 0 {
			m.history = append(m.history, msg.newMessages...)
			if m.sessions != nil && m.sessionID != "" {
				nodeID, err := m.sessions.AppendMessages(m.sessionID, m.currentNodeID, msg.newMessages)
				if err != nil {
					m.lines = append(m.lines, errorStyle.Render("session write failed: "+err.Error()))
				} else {
					m.currentNodeID = nodeID
				}
			}
		}
		finalAssistant := lastAssistantContent(msg.newMessages)
		if m.assistantLineIdx >= 0 && m.assistantLineIdx < len(m.lines) {
			prefix := assistantLine("")
			if strings.TrimSpace(strings.TrimPrefix(m.lines[m.assistantLineIdx], prefix)) == "" && strings.TrimSpace(finalAssistant) != "" {
				m.lines[m.assistantLineIdx] = assistantLine(finalAssistant)
			}
		}
		m.assistantLineIdx = -1
		if m.runtime != nil {
			return m, fireEventCmd(m.runtime, luaext.Event{Kind: luaext.EventTurnEnd})
		}
		return m, nil
	case turnErrMsg:
		m.running = false
		m.assistantLineIdx = -1
		m.lines = append(m.lines, errorStyle.Render("Error: "+msg.err.Error()))
		return m, nil
	case extCommandDoneMsg:
		if msg.err != nil {
			m.lines = append(m.lines, errorStyle.Render("command error: "+msg.err.Error()))
		}
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
	// Show suggestions only when typing a command name (starts with / but no space yet).
	if strings.HasPrefix(val, "/") && !strings.Contains(val, " ") {
		m.suggestions = filterCommands(val, m.resourceCmds)
		if m.suggCursor >= len(m.suggestions) {
			m.suggCursor = max(0, len(m.suggestions)-1)
		}
	} else {
		m.suggestions = nil
		m.suggCursor = 0
	}
	return m
}

func (m Model) applySuggestion() Model {
	if len(m.suggestions) == 0 || m.suggCursor >= len(m.suggestions) {
		return m
	}
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

func (m Model) submitPrompt(value string) (tea.Model, tea.Cmd) {
	if m.agent == nil {
		m.lines = append(m.lines, errorStyle.Render("Error: agent not initialized"))
		return m, nil
	}

	m.lines = append(m.lines, userLine(value))
	m.lines = append(m.lines, assistantLine(""))
	m.assistantLineIdx = len(m.lines) - 1
	m.running = true
	m.streamCh = make(chan tea.Msg, 128)
	history := append([]openrouter.Message{}, m.history...)
	// Prepend system prompt as first message each turn (not persisted to session).
	if sp := strings.TrimSpace(m.systemPrompt); sp != "" {
		history = append([]openrouter.Message{{Role: "system", Content: sp}}, history...)
	}
	rt := m.runtime // capture pointer; safe to call from agent goroutine

	go func(ch chan<- tea.Msg, input, modelName string, hist []openrouter.Message) {
		newMessages, err := m.agent.RunTurn(context.Background(), modelName, hist, input, agent.TurnCallbacks{
			OnToken: func(token string) {
				ch <- tokenMsg{token: token}
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
	}(m.streamCh, value, m.modelName, history)

	return m, waitForStream(m.streamCh)
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
		return m, tea.Quit

	case "/model":
		if len(parts) >= 2 {
			// Direct set by id — skip picker.
			id := parts[1]
			m.modelName = id
			if m.sessions != nil && m.sessionID != "" {
				if err := m.sessions.SetSessionModel(m.sessionID, m.modelName); err != nil {
					m.lines = append(m.lines, errorStyle.Render("failed to persist model: "+err.Error()))
				}
			}
			m.lines = append(m.lines, metaStyle.Render("Model set to "+m.modelName))
			return m, nil
		}
		// Open interactive picker.
		if m.catalog == nil {
			m.lines = append(m.lines, errorStyle.Render("model catalog not available"))
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
			m.lines = append(m.lines, errorStyle.Render("Session store not available"))
			return m, nil
		}
		sessionID, err := m.sessions.NewSession()
		if err != nil {
			m.lines = append(m.lines, errorStyle.Render("Failed to create session: "+err.Error()))
			return m, nil
		}
		m.sessionID = sessionID
		m.currentNodeID = ""
		m.history = nil
		if m.sessions != nil && m.modelName != "" {
			if err := m.sessions.SetSessionModel(m.sessionID, m.modelName); err != nil {
				m.lines = append(m.lines, errorStyle.Render("Failed to persist session model: "+err.Error()))
			}
		}
		m.lines = introLines(m.sessionID, m.currentNodeID)
		m.lines = append(m.lines, metaStyle.Render("Started new session: "+m.sessionID))
		return m, nil

	case "/sessions":
		if m.sessions == nil {
			m.lines = append(m.lines, errorStyle.Render("Session store not available"))
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
			m.lines = append(m.lines, errorStyle.Render("Session store not available"))
			return m, nil
		}
		if m.sessionID == "" {
			m.lines = append(m.lines, errorStyle.Render("No active session"))
			return m, nil
		}
		label := strings.Join(parts[1:], " ")
		if label == "" {
			// Show current label.
			current, err := m.sessions.GetSessionLabel(m.sessionID)
			if err != nil {
				m.lines = append(m.lines, errorStyle.Render("Failed to read label: "+err.Error()))
				return m, nil
			}
			if current == "" {
				m.lines = append(m.lines, metaStyle.Render("No label set. Use /label <text> to set one."))
			} else {
				m.lines = append(m.lines, metaStyle.Render("Label: "+current))
			}
			return m, nil
		}
		if err := m.sessions.SetSessionLabel(m.sessionID, label); err != nil {
			m.lines = append(m.lines, errorStyle.Render("Failed to set label: "+err.Error()))
			return m, nil
		}
		m.lines = append(m.lines, metaStyle.Render("Session labelled: "+label))
		return m, nil

	case "/system":
		text := strings.Join(parts[1:], " ")
		if text == "" {
			if m.systemPrompt == "" {
				m.lines = append(m.lines, metaStyle.Render("No system prompt set. Use /system <text> to set one."))
			} else {
				m.lines = append(m.lines, metaStyle.Render("System prompt: "+m.systemPrompt))
			}
			return m, nil
		}
		m.systemPrompt = strings.TrimSpace(text)
		m.lines = append(m.lines, metaStyle.Render("System prompt updated."))
		return m, nil

	case "/tree":
		if m.sessions == nil {
			m.lines = append(m.lines, errorStyle.Render("Session store not available"))
			return m, nil
		}
		if m.sessionID == "" {
			m.lines = append(m.lines, metaStyle.Render("No active session. Use /resume first."))
			return m, nil
		}
		nodes, err := m.sessions.ListNodes(m.sessionID)
		if err != nil {
			m.lines = append(m.lines, errorStyle.Render("Failed to load tree: "+err.Error()))
			return m, nil
		}
		if len(nodes) == 0 {
			m.lines = append(m.lines, metaStyle.Render("Session tree is empty"))
			return m, nil
		}
		m.lines = append(m.lines, metaStyle.Render("Session tree:"))
		m.lines = append(m.lines, renderTree(nodes, m.currentNodeID)...)
		m.lines = append(m.lines, metaStyle.Render("Resume from node: /resume <session-id> <node-id-prefix>"))
		return m, nil

	default:
		cmd := parts[0]

		// /skill:<name>  — inject skill as a system message into history
		if strings.HasPrefix(cmd, "/skill:") {
			skillName := strings.TrimPrefix(cmd, "/skill:")
			if m.registry == nil {
				m.lines = append(m.lines, errorStyle.Render("no resource registry loaded"))
				return m, nil
			}
			skill, ok := m.registry.GetSkill(skillName)
			if !ok {
				m.lines = append(m.lines, errorStyle.Render("skill not found: "+skillName))
				return m, nil
			}
			sysMsg := openrouter.Message{Role: "system", Content: skill.Content}
			m.history = append(m.history, sysMsg)
			if m.sessions != nil && m.sessionID != "" {
				if _, err := m.sessions.AppendMessages(m.sessionID, m.currentNodeID, []openrouter.Message{sysMsg}); err != nil {
					m.lines = append(m.lines, errorStyle.Render("session write failed: "+err.Error()))
				}
			}
			m.lines = append(m.lines, metaStyle.Render("skill loaded: "+skillName))
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

		m.lines = append(m.lines, errorStyle.Render("unknown command: "+cmd))
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
	return m.viewChat(header)
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
	// One reserved line between viewport and input shows scroll position when
	// the user has scrolled up; blank otherwise so layout never jumps.
	scrollLine := " "
	if !m.vp.AtBottom() {
		below := m.vp.TotalLineCount() - m.vp.YOffset - m.vp.Height
		if below > 0 {
			scrollLine = metaStyle.Render(fmt.Sprintf("  ↓ %d more", below))
		}
	}

	parts := []string{header, "", m.vp.View(), scrollLine}
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
	var b strings.Builder
	for i, cmd := range m.suggestions {
		selected := i == m.suggCursor

		label := cmd.name
		if cmd.args != "" {
			label += " " + suggDimStyle.Render(cmd.args)
		}
		desc := suggDimStyle.Render("  — " + cmd.desc)

		if selected {
			b.WriteString(suggSelectedStyle.Render("▶ " + cmd.name))
			if cmd.args != "" {
				b.WriteString(suggSelectedDimStyle.Render(" " + cmd.args))
			}
			b.WriteString(suggSelectedDimStyle.Render("  — " + cmd.desc))
		} else {
			b.WriteString("  " + label + desc)
		}
		b.WriteString("\n")
	}
	out := strings.TrimRight(b.String(), "\n")
	return out
}

// ── helpers ────────────────────────────────────────────────────────────────────

// recalcViewport adjusts viewport dimensions to fit the current chrome (header,
// scroll indicator, suggestions, input, optional status bar) and scrolls to the
// bottom when autoScroll is true.
func (m Model) recalcViewport() Model {
	chrome := 4 + len(m.suggestions) // header(1) + blank(1) + scrollLine(1) + input(1)
	if len(m.statuses) > 0 {
		chrome++ // status bar
	}
	m.vp.Width = m.width
	m.vp.Height = max(3, m.height-chrome)
	if m.autoScroll {
		m.vp.GotoBottom()
	}
	return m
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

func introLines(sessionID, nodeID string) []string {
	lines := []string{"Welcome to pigeon.", "Type /quit to exit."}
	if sessionID != "" {
		lines = append(lines, metaStyle.Render("Session: "+sessionID))
	}
	if nodeID != "" {
		lines = append(lines, metaStyle.Render("Node: "+shortID(nodeID)))
	}
	return lines
}

func userLine(content string) string    { return userStyle.Render("You:") + " " + content }
func assistantLine(content string) string { return asstStyle.Render("Assistant:") + " " + content }

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

func renderHistoryLines(messages []openrouter.Message) []string {
	out := make([]string, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case "user":
			if strings.TrimSpace(msg.Content) != "" {
				out = append(out, userLine(msg.Content))
			}
		case "assistant":
			if strings.TrimSpace(msg.Content) != "" {
				out = append(out, assistantLine(msg.Content))
			}
			for _, tc := range msg.ToolCalls {
				out = append(out, metaStyle.Render(fmt.Sprintf("↳ tool call: %s(%s)", tc.Function.Name, tc.Function.Arguments)))
			}
		case "tool":
			name := msg.Name
			if strings.TrimSpace(name) == "" {
				name = "tool"
			}
			out = append(out, metaStyle.Render(fmt.Sprintf("↳ tool result (%s): %s", name, summarize(msg.Content))))
		}
	}
	return out
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
