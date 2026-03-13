package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type turnRunner interface {
	RunTurn(ctx context.Context, model string, userInput string, onToken func(string)) (string, error)
}

type tokenMsg struct {
	token string
}

type turnDoneMsg struct {
	response string
}

type turnErrMsg struct {
	err error
}

type Model struct {
	agent turnRunner

	modelName string
	input     textinput.Model
	lines     []string
	streamCh  chan tea.Msg
	running   bool
	width     int
	height    int
}

var (
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	userStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10"))
	asstStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))
)

func NewModel(agent turnRunner, modelName string) Model {
	in := textinput.New()
	in.Placeholder = "Ask pigeon..."
	in.Prompt = "> "
	in.Focus()
	in.CharLimit = 0
	in.Width = 100

	return Model{
		agent:     agent,
		modelName: strings.TrimSpace(modelName),
		input:     in,
		lines: []string{
			"Welcome to pigeon.",
			"Type /quit to exit.",
		},
	}
}

func (m Model) Init() tea.Cmd {
	return textinput.Blink
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

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.input.Width = max(20, msg.Width-4)
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
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

			if strings.HasPrefix(value, "/") {
				return m.handleCommand(value)
			}
			if m.agent == nil {
				m.lines = append(m.lines, errorStyle.Render("Error: agent not initialized"))
				return m, nil
			}

			m.lines = append(m.lines, userStyle.Render("You:")+" "+value)
			m.lines = append(m.lines, asstStyle.Render("Assistant:")+" ")
			m.running = true
			m.streamCh = make(chan tea.Msg, 32)

			go func(ch chan<- tea.Msg, input, modelName string) {
				final, err := m.agent.RunTurn(context.Background(), modelName, input, func(token string) {
					ch <- tokenMsg{token: token}
				})
				if err != nil {
					ch <- turnErrMsg{err: err}
					close(ch)
					return
				}
				ch <- turnDoneMsg{response: final}
				close(ch)
			}(m.streamCh, value, m.modelName)

			return m, waitForStream(m.streamCh)
		}
	case tokenMsg:
		if len(m.lines) == 0 {
			m.lines = append(m.lines, asstStyle.Render("Assistant:")+" ")
		}
		m.lines[len(m.lines)-1] += msg.token
		return m, waitForStream(m.streamCh)
	case turnDoneMsg:
		m.running = false
		if len(m.lines) == 0 {
			m.lines = append(m.lines, asstStyle.Render("Assistant:")+" "+msg.response)
		} else if strings.TrimSpace(strings.TrimPrefix(m.lines[len(m.lines)-1], asstStyle.Render("Assistant:"))) == "" {
			m.lines[len(m.lines)-1] = asstStyle.Render("Assistant:") + " " + msg.response
		}
		return m, nil
	case turnErrMsg:
		m.running = false
		m.lines = append(m.lines, errorStyle.Render("Error: "+msg.err.Error()))
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m Model) handleCommand(raw string) (tea.Model, tea.Cmd) {
	parts := strings.Fields(raw)
	if len(parts) == 0 {
		return m, nil
	}
	switch parts[0] {
	case "/quit":
		return m, tea.Quit
	case "/model":
		if len(parts) < 2 {
			m.lines = append(m.lines, "Current model: "+m.modelName+" (set with /model <id>)")
			return m, nil
		}
		m.modelName = parts[1]
		m.lines = append(m.lines, fmt.Sprintf("Model set to %s", m.modelName))
		return m, nil
	default:
		m.lines = append(m.lines, "Unknown command: "+parts[0])
		return m, nil
	}
}

func (m Model) View() string {
	status := "idle"
	if m.running {
		status = "streaming"
	}

	header := headerStyle.Render(fmt.Sprintf("pigeon • model=%s • %s", m.modelName, status))
	body := strings.Join(m.lines, "\n")
	if body == "" {
		body = "(no messages yet)"
	}

	return lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		"",
		body,
		"",
		m.input.View(),
	)
}
