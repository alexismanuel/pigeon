package tui

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"pigeon/internal/auth"
)

// ── login modes ───────────────────────────────────────────────────────────────

// loginSelectMode and loginAuthMode are appMode values injected by init().
// They are declared as variables rather than iota constants so this file
// does not need to modify the const block in model.go.
var (
	loginSelectMode appMode
	loginAuthMode   appMode
)

func init() {
	// Claim the next two free mode slots after permissionMode (= 4).
	loginSelectMode = permissionMode + 1
	loginAuthMode   = permissionMode + 2
}

// Chrome heights (lines) consumed by the login overlays.
const (
	loginSelectChrome = 9  // border×2 + title + blank + providers + blank + hint
	loginAuthChrome   = 11 // border×2 + title + blank + up-to-5 message lines + blank + hint
)

// ── provider registry ─────────────────────────────────────────────────────────

// loginProvider describes one OAuth-capable provider.
type loginProvider struct {
	id   string
	name string
}

// oauthProviders is the ordered list of providers that support OAuth login.
var oauthProviders = []loginProvider{
	{id: "anthropic", name: "Anthropic (Claude Pro / Max)"},
}

// unauthenticatedProviders returns the subset of oauthProviders for which no
// credentials are currently stored.
func unauthenticatedProviders() []loginProvider {
	d, err := auth.Load()
	if err != nil {
		// If we can't read auth.json, show all providers.
		return oauthProviders
	}
	var out []loginProvider
	for _, p := range oauthProviders {
		cred, ok := d.Providers[p.id]
		if !ok || cred.Type == "" {
			out = append(out, p)
			continue
		}
		// For OAuth providers: show if token is absent or expired with no refresh.
		if cred.Type == "oauth" && cred.OAuth == nil {
			out = append(out, p)
		}
	}
	return out
}

// ── tea messages ─────────────────────────────────────────────────────────────

type loginEventMsg struct {
	kind    string // "url" | "progress" | "done" | "err"
	url     string // set for kind == "url"
	message string // set for kind == "progress" | "err" | "done"
	err     error  // set for kind == "err"
}

// waitForLoginEvent returns a Cmd that reads the next event from ch.
func waitForLoginEvent(ch <-chan loginEventMsg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

// ── state helpers (stored on Model, see model.go) ─────────────────────────────

// enterLoginSelect switches to provider-selection mode. It returns the new
// provider list and resets the cursor, plus the tea.Cmd to run.
func (m Model) enterLoginSelect() (Model, tea.Cmd) {
	providers := unauthenticatedProviders()
	if len(providers) == 0 {
		m.appendBlock(chatBlock{
			kind:    bMeta,
			content: "✓ Already logged in to all supported providers.",
		})
		return m, textinput.Blink
	}
	m.loginProviders = providers
	m.loginSelectIdx = 0
	m.mode = loginSelectMode
	m.input.Blur()
	return m, nil
}

// startLoginAuth starts the OAuth goroutine for the provider at loginSelectIdx.
func (m Model) startLoginAuth() (Model, tea.Cmd) {
	if m.loginSelectIdx >= len(m.loginProviders) {
		return m, nil
	}
	p := m.loginProviders[m.loginSelectIdx]

	m.loginLines = []string{fmt.Sprintf("Connecting to %s…", p.name)}
	m.loginCh = make(chan loginEventMsg, 32)
	m.mode = loginAuthMode

	ctx, cancel := context.WithCancel(context.Background())
	m.loginCancel = cancel

	ch := m.loginCh
	go func() {
		defer close(ch)

		switch p.id {
		case "anthropic":
			runAnthropicLogin(ctx, ch)
		default:
			ch <- loginEventMsg{kind: "err", message: fmt.Sprintf("unknown provider: %s", p.id)}
		}
	}()

	return m, waitForLoginEvent(m.loginCh)
}

// runAnthropicLogin drives the full Anthropic OAuth flow and sends events to ch.
func runAnthropicLogin(ctx context.Context, ch chan<- loginEventMsg) {
	creds, err := auth.Login(ctx, auth.LoginCallbacks{
		OnAuthURL: func(authURL string) {
			ch <- loginEventMsg{kind: "url", url: authURL}
			openBrowserForLogin(authURL)
		},
		OnProgress: func(msg string) {
			ch <- loginEventMsg{kind: "progress", message: msg}
		},
	})
	if err != nil {
		if ctx.Err() != nil {
			ch <- loginEventMsg{kind: "err", message: "Login cancelled."}
		} else {
			ch <- loginEventMsg{kind: "err", message: "Login failed: " + err.Error()}
		}
		return
	}

	if err := auth.SetAnthropicOAuth(creds); err != nil {
		ch <- loginEventMsg{kind: "err", message: "Failed to save credentials: " + err.Error()}
		return
	}

	ch <- loginEventMsg{kind: "done", message: "✓ Logged in to Anthropic. Credentials saved."}
}

// openBrowserForLogin tries to open the given URL in the default browser.
func openBrowserForLogin(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

// ── Update handlers ────────────────────────────────────────────────────────────

// updateLoginSelect handles input in the provider-selection overlay.
func (m Model) updateLoginSelect(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "esc", "ctrl+c":
		return m.closeLoginSelect()

	case "up", "ctrl+p", "k":
		if m.loginSelectIdx > 0 {
			m.loginSelectIdx--
		}
		return m, nil

	case "down", "ctrl+n", "j":
		if m.loginSelectIdx < len(m.loginProviders)-1 {
			m.loginSelectIdx++
		}
		return m, nil

	case "enter":
		return m.startLoginAuth()
	}
	return m, nil
}

// updateLoginAuth handles input and incoming events during the OAuth flow.
func (m Model) updateLoginAuth(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "ctrl+c":
			if m.loginCancel != nil {
				m.loginCancel()
			}
			// Drain the channel so the goroutine can exit cleanly.
			go func(ch <-chan loginEventMsg) {
				for range ch {
				}
			}(m.loginCh)
			return m.closeLoginAuth("Login cancelled.")
		}
		return m, nil

	case loginEventMsg:
		switch msg.kind {
		case "url":
			// Show URL with a terminal hyperlink when possible.
			hyperlink := termHyperlink(msg.url, "click to open in browser")
			m.loginLines = append(m.loginLines,
				"Open this URL in your browser:",
				"  "+hyperlink,
				"  (or copy: "+truncURL(msg.url, 60)+")",
			)
			return m, waitForLoginEvent(m.loginCh)

		case "progress":
			m.loginLines = append(m.loginLines, msg.message)
			return m, waitForLoginEvent(m.loginCh)

		case "done":
			if m.loginCancel != nil {
				m.loginCancel()
			}
			return m.closeLoginAuth(msg.message)

		case "err":
			if m.loginCancel != nil {
				m.loginCancel()
			}
			return m.closeLoginAuthErr(msg.message)
		}
		return m, waitForLoginEvent(m.loginCh)
	}
	return m, nil
}

func (m Model) closeLoginSelect() (tea.Model, tea.Cmd) {
	m.mode = chatMode
	m.loginProviders = nil
	m.input.Focus()
	return m, textinput.Blink
}

func (m Model) closeLoginAuth(successMsg string) (tea.Model, tea.Cmd) {
	// Fire the hot-add callback before clearing state so we still have the
	// provider ID available.
	if m.onProviderLogin != nil && len(m.loginProviders) > 0 && m.loginSelectIdx < len(m.loginProviders) {
		providerID := m.loginProviders[m.loginSelectIdx].id
		m.onProviderLogin(providerID)
	}
	m.mode = chatMode
	m.loginLines = nil
	m.loginCh = nil
	m.loginCancel = nil
	m.loginProviders = nil
	m.input.Focus()
	if successMsg != "" {
		m.appendBlock(chatBlock{kind: bMeta, content: successMsg})
	}
	return m, textinput.Blink
}

func (m Model) closeLoginAuthErr(errMsg string) (tea.Model, tea.Cmd) {
	m.mode = chatMode
	m.loginLines = nil
	m.loginCh = nil
	m.loginCancel = nil
	m.loginProviders = nil
	m.input.Focus()
	if errMsg != "" {
		m.appendBlock(chatBlock{kind: bError, content: errMsg})
	}
	return m, textinput.Blink
}

// ── View helpers ──────────────────────────────────────────────────────────────

// renderLoginSelect renders the provider-selection dialog.
func (m Model) renderLoginSelect() string {
	dialogW := m.width - 4
	if dialogW < 40 {
		dialogW = 40
	}
	innerW := dialogW - 4

	var b strings.Builder
	b.WriteString(loginTitleStyle.Render("🔑 Login") + "\n")
	b.WriteString("\n")
	b.WriteString(loginLabelStyle.Render("Select a provider to authenticate:") + "\n")
	b.WriteString("\n")

	for i, p := range m.loginProviders {
		if i == m.loginSelectIdx {
			b.WriteString(loginSelectedStyle.Render("▶ "+p.name) + "\n")
		} else {
			b.WriteString("  " + loginNormalStyle.Render(p.name) + "\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(loginHintStyle.Render("↑↓ navigate  •  enter select  •  esc cancel"))

	box := loginBorderStyle.Width(innerW).Render(b.String())
	return lipgloss.NewStyle().Width(m.width).Render(box)
}

// renderLoginAuth renders the OAuth-flow progress dialog.
func (m Model) renderLoginAuth() string {
	dialogW := m.width - 4
	if dialogW < 40 {
		dialogW = 40
	}
	innerW := dialogW - 4

	var b strings.Builder
	title := "🔑 Login"
	if len(m.loginProviders) > 0 && m.loginSelectIdx < len(m.loginProviders) {
		title = fmt.Sprintf("🔑 Login to %s", m.loginProviders[m.loginSelectIdx].name)
	}
	b.WriteString(loginTitleStyle.Render(title) + "\n")
	b.WriteString("\n")

	// Show up to the last 5 lines so the dialog doesn't grow beyond fixed chrome.
	lines := m.loginLines
	const maxLines = 5
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	for _, l := range lines {
		// Wrap/truncate long lines (e.g. URLs).
		if lipgloss.Width(l) > innerW {
			l = l[:innerW-1] + "…"
		}
		b.WriteString(loginProgressStyle.Render(l) + "\n")
	}

	b.WriteString("\n")
	b.WriteString(loginHintStyle.Render("esc to cancel"))

	box := loginBorderStyle.Width(innerW).Render(b.String())
	return lipgloss.NewStyle().Width(m.width).Render(box)
}

// ── view integration ──────────────────────────────────────────────────────────

// viewChatWithLoginSelect renders the chat viewport with the provider-selector
// overlay at the bottom.
func (m Model) viewChatWithLoginSelect(header string) string {
	var statusLine string
	if below := m.vp.TotalLineCount() - m.vp.YOffset - m.vp.Height; below > 0 {
		statusLine = metaStyle.Render(fmt.Sprintf("  ↓ %d more  ", below))
	}
	if statusLine == "" {
		statusLine = " "
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, "", m.vp.View(), statusLine, m.renderLoginSelect())
}

// viewChatWithLoginAuth renders the chat viewport with the auth-flow overlay.
func (m Model) viewChatWithLoginAuth(header string) string {
	var statusLine string
	if below := m.vp.TotalLineCount() - m.vp.YOffset - m.vp.Height; below > 0 {
		statusLine = metaStyle.Render(fmt.Sprintf("  ↓ %d more  ", below))
	}
	if statusLine == "" {
		statusLine = " "
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, "", m.vp.View(), statusLine, m.renderLoginAuth())
}

// ── helpers ───────────────────────────────────────────────────────────────────

// termHyperlink returns an OSC 8 hyperlink escape sequence.
// Terminals that don't support it render the label as plain text.
func termHyperlink(url, label string) string {
	return fmt.Sprintf("\x1b]8;;%s\x07%s\x1b]8;;\x07", url, label)
}

// truncURL truncates a URL to maxLen characters, appending "…".
func truncURL(u string, maxLen int) string {
	if len(u) <= maxLen {
		return u
	}
	return u[:maxLen-1] + "…"
}

// ── styles ────────────────────────────────────────────────────────────────────

var (
	loginBorderStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("12")).Padding(0, 1)
	loginTitleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	loginLabelStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	loginSelectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	loginNormalStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	loginProgressStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	loginHintStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)
)
