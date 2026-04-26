package tui

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"pigeon/internal/auth"
)

// ── login modes ───────────────────────────────────────────────────────────────

// loginSelectMode and loginAuthMode are appMode values injected by init().
// They are declared as variables rather than iota constants so this file
// does not need to modify the const block in model.go.
var (
	loginSelectMode  appMode
	loginAuthMode    appMode
	loginApiKeyMode  appMode
)

func init() {
	// Claim the next free mode slots after permissionMode (= 4).
	loginSelectMode = permissionMode + 1
	loginAuthMode   = permissionMode + 2
	loginApiKeyMode = permissionMode + 3
}

// Chrome heights (lines) consumed by the login overlays.
const (
	loginSelectChrome = 9  // border×2 + title + blank + providers + blank + hint
	loginAuthChrome   = 11 // border×2 + title + blank + up-to-5 message lines + blank + hint
	loginApiKeyChrome = 9  // border×2 + title + blank + instructions + input + blank + hint
)

// ── provider registry ─────────────────────────────────────────────────────────

// loginProvider describes one login-capable provider.
type loginProvider struct {
	id       string
	name     string
	authType string // "oauth" or "api_key"
}

// oauthProviders is the ordered list of providers that support login.
var oauthProviders = []loginProvider{
	{id: "zai", name: "Zai (Coding Plan Subscription)", authType: "api_key"},
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
		return m, nil
	}
	m.loginProviders = providers
	m.loginSelectIdx = 0
	m.mode = loginSelectMode
	m.input.Blur()
	return m, nil
}

// startLoginAuth starts the login flow for the provider at loginSelectIdx.
func (m Model) startLoginAuth() (Model, tea.Cmd) {
	if m.loginSelectIdx >= len(m.loginProviders) {
		return m, nil
	}
	p := m.loginProviders[m.loginSelectIdx]

	switch p.authType {
	case "api_key":
		return m.enterLoginApiKey(p)
	case "oauth":
		return m.startOAuthFlow(p)
	default:
		m.appendBlock(chatBlock{kind: bError, content: fmt.Sprintf("unknown auth type: %s", p.authType)})
		return m, m.input.Focus()
	}
}

// enterLoginApiKey switches to API-key input mode for the given provider.
func (m Model) enterLoginApiKey(p loginProvider) (Model, tea.Cmd) {
	m.loginLines = []string{
		fmt.Sprintf("Enter your %s API key.", p.name),
		"Get your key at: https://zai.chat → Profile → API Key",
	}
	m.loginInput.SetValue("")
	m.mode = loginApiKeyMode
	return m, m.loginInput.Focus()
}

// startOAuthFlow starts the OAuth goroutine for the given provider.
func (m Model) startOAuthFlow(p loginProvider) (Model, tea.Cmd) {
	m.loginLines = []string{fmt.Sprintf("Connecting to %s…", p.name)}
	m.loginCh = make(chan loginEventMsg, 32)
	m.mode = loginAuthMode

	ch := m.loginCh
	go func() {
		defer close(ch)
		ch <- loginEventMsg{kind: "err", message: fmt.Sprintf("OAuth not supported for provider: %s", p.id)}
	}()

	return m, waitForLoginEvent(m.loginCh)
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
	key, ok := msg.(tea.KeyPressMsg)
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
	case tea.KeyPressMsg:
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

// updateLoginApiKey handles input in the API-key entry overlay.
func (m Model) updateLoginApiKey(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc", "ctrl+c":
			return m.closeLoginApiKey("Login cancelled.")
		case "enter":
			key := strings.TrimSpace(m.loginInput.Value())
			if key == "" {
				return m, nil // ignore empty submit
			}
			if len(m.loginProviders) == 0 || m.loginSelectIdx >= len(m.loginProviders) {
				return m.closeLoginApiKey("")
			}
			p := m.loginProviders[m.loginSelectIdx]
			switch p.id {
			case "zai":
				if err := auth.SetZaiAPIKey(key); err != nil {
					return m.closeLoginApiKeyErr(fmt.Sprintf("Failed to save Zai API key: %v", err))
				}
				// Hot-add the Zai provider.
				if m.onProviderLogin != nil {
					m.onProviderLogin("zai")
				}
				return m.closeLoginApiKey("✓ Zai API key saved. You can now use Zai models.")
			default:
				return m.closeLoginApiKeyErr(fmt.Sprintf("API key login not supported for %s", p.id))
			}
		}
	}

	// Forward other messages (text input updates, etc.) to the login input.
	var cmd tea.Cmd
	m.loginInput, cmd = m.loginInput.Update(msg)
	return m, cmd
}

func (m Model) closeLoginApiKey(successMsg string) (tea.Model, tea.Cmd) {
	m.mode = chatMode
	m.loginLines = nil
	m.loginInput.SetValue("")
	m.loginInput.Blur()
	m.loginProviders = nil
	if successMsg != "" {
		m.appendBlock(chatBlock{kind: bMeta, content: successMsg})
	}
	return m, m.input.Focus()
}

func (m Model) closeLoginApiKeyErr(errMsg string) (tea.Model, tea.Cmd) {
	m.mode = chatMode
	m.loginLines = nil
	m.loginInput.SetValue("")
	m.loginInput.Blur()
	m.loginProviders = nil
	if errMsg != "" {
		m.appendBlock(chatBlock{kind: bError, content: errMsg})
	}
	return m, m.input.Focus()
}

func (m Model) closeLoginSelect() (tea.Model, tea.Cmd) {
	m.mode = chatMode
	m.loginProviders = nil
	return m, m.input.Focus()
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
	if successMsg != "" {
		m.appendBlock(chatBlock{kind: bMeta, content: successMsg})
	}
	return m, m.input.Focus()
}

func (m Model) closeLoginAuthErr(errMsg string) (tea.Model, tea.Cmd) {
	m.mode = chatMode
	m.loginLines = nil
	m.loginCh = nil
	m.loginCancel = nil
	m.loginProviders = nil
	if errMsg != "" {
		m.appendBlock(chatBlock{kind: bError, content: errMsg})
	}
	return m, m.input.Focus()
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

// renderLoginApiKey renders the API-key entry dialog.
func (m Model) renderLoginApiKey() string {
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

	for _, l := range m.loginLines {
		if lipgloss.Width(l) > innerW {
			l = l[:innerW-1] + "…"
		}
		b.WriteString(loginProgressStyle.Render(l) + "\n")
	}

	b.WriteString("\n")
	b.WriteString(m.loginInput.View() + "\n")
	b.WriteString(loginHintStyle.Render("enter to submit  •  esc to cancel"))

	box := loginBorderStyle.Width(innerW).Render(b.String())
	return lipgloss.NewStyle().Width(m.width).Render(box)
}

// ── view integration ──────────────────────────────────────────────────────────

// viewChatWithLoginSelect renders the chat viewport with the provider-selector
// overlay at the bottom.
func (m Model) viewChatWithLoginSelect(header string) string {
	var statusLine string
	if below := m.vp.TotalLineCount() - m.vp.YOffset() - m.vp.Height(); below > 0 {
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
	if below := m.vp.TotalLineCount() - m.vp.YOffset() - m.vp.Height(); below > 0 {
		statusLine = metaStyle.Render(fmt.Sprintf("  ↓ %d more  ", below))
	}
	if statusLine == "" {
		statusLine = " "
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, "", m.vp.View(), statusLine, m.renderLoginAuth())
}

// viewChatWithLoginApiKey renders the chat viewport with the API-key entry overlay.
func (m Model) viewChatWithLoginApiKey(header string) string {
	var statusLine string
	if below := m.vp.TotalLineCount() - m.vp.YOffset() - m.vp.Height(); below > 0 {
		statusLine = metaStyle.Render(fmt.Sprintf("  ↓ %d more  ", below))
	}
	if statusLine == "" {
		statusLine = " "
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, "", m.vp.View(), statusLine, m.renderLoginApiKey())
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

// handleLogout removes credentials for the given provider (or all if none specified).
func (m Model) handleLogout(args []string) (tea.Model, tea.Cmd) {
	if len(args) == 0 {
		m.appendBlock(chatBlock{kind: bError, content: "Usage: /logout <provider>  (e.g. /logout zai)"})
		return m, m.input.Focus()
	}
	provider := args[0]
	if err := auth.RemoveProvider(provider); err != nil {
		m.appendBlock(chatBlock{kind: bError, content: fmt.Sprintf("Failed to logout %s: %v", provider, err)})
		return m, m.input.Focus()
	}
	m.appendBlock(chatBlock{kind: bAssistant, content: fmt.Sprintf("Logged out %s.", provider)})
	return m, m.input.Focus()
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
