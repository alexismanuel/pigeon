package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	tea "charm.land/bubbletea/v2"

	"pigeon/internal/agent"
	"pigeon/internal/app"
	"pigeon/internal/auth"
	"pigeon/internal/config"
	zaiclient "pigeon/internal/provider/zai"
	luaext "pigeon/internal/extensions/lua"
	"pigeon/internal/permission"
	"pigeon/internal/resources"
	"pigeon/internal/session"
	"pigeon/internal/tools"
	"pigeon/internal/tui"
)

func main() {
	// ── chat flags ────────────────────────────────────────────────────────────
	model := flag.String("model", "", "Model ID (e.g. claude-sonnet-4-6, openai/gpt-4o-mini)")
	systemFlag := flag.String("system", "", "system prompt (overrides ~/.config/pigeon/system.md and .pigeon/system.md)")
	flag.Parse()

	systemPrompt := config.ResolveSystemPrompt(*systemFlag)
	settings := config.LoadSettings()

	// Build multi-provider (OpenRouter + Zai + LM Studio as available).
	mp, err := app.BuildProviders(os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pigeon: %v\n", err)
		os.Exit(1)
	}

	// Choose a sensible default model.
	modelName := *model
	if modelName == "" {
		modelName = defaultModel()
	}

	// Build the permission service and wire it into the executor.
	workingDir, _ := os.Getwd()
	permService := permission.NewService(
		workingDir,
		settings.Permissions.SkipRequests,
		settings.Permissions.AllowedTools,
		settings.Permissions.BashDenyPatterns,
		settings.Permissions.SandboxMode,
	)
	executor := tools.NewExecutorWithPermissions(permService)
	ag := agent.NewWithTools(mp, executor)

	sessionManager := session.NewManager("")
	if _, err := sessionManager.PruneEmptySessions(); err != nil {
		fmt.Fprintf(os.Stderr, "pigeon: warning: failed to prune empty sessions: %v\n", err)
	}
	sessionID, err := sessionManager.NewSession()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pigeon: warning: failed to initialize session file: %v\n", err)
		sessionID = ""
	}

	if sessionID != "" {
		if err := sessionManager.SetSessionModel(sessionID, modelName); err != nil {
			fmt.Fprintf(os.Stderr, "pigeon: warning: failed to persist initial model: %v\n", err)
		}
	}

	reg, err := resources.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pigeon: warning: failed to load resources: %v\n", err)
	}

	statusCh := make(chan luaext.StatusUpdate, 64)
	runtime := luaext.NewRuntime(statusCh)
	runtime.SessionModelFn = func() string {
		m, _ := sessionManager.GetSessionModel(sessionID)
		return m
	}
	if reg != nil {
		for _, ext := range reg.ListExtensionPaths() {
			if err := runtime.Load(ext.Name, ext.Path); err != nil {
				fmt.Fprintf(os.Stderr, "pigeon: warning: extension %s: %v\n", ext.Name, err)
			}
		}
	}

	// onProviderLogin is called after a successful in-TUI OAuth login.
	// It hot-adds the freshly authenticated provider to the multi-provider so
	// models appear in the picker immediately without a restart.
	onProviderLogin := func(providerID string) {
		switch providerID {
		case "zai":
			key, err := auth.GetZaiAPIKey()
			if err == nil && key != "" {
				mp.Add("zai", zaiclient.NewClient(key, nil))
			}
		}
	}

	m := tui.NewModel(ag, mp, modelName, sessionManager, sessionID, reg, runtime, statusCh, settings, permService, onProviderLogin, systemPrompt)

	// bubbletea v2 always enables modifyOtherKeys + kitty keyboard protocol
	// using the SET form (\x1b[=N;1u). WezTerm uses a stack-based model where
	// only PUSH (\x1b[>Nu) and POP (\x1b[<u) are tracked. The SET form is
	// silently ignored, so bubbletea's cleanup (\x1b[=0;1u) never takes effect.
	//
	// We wrap stdout with a translator that converts bubbletea's SET sequences
	// to PUSH/POP equivalents so WezTerm's stack is correctly managed:
	//   \x1b[=N;1u  →  \x1b[>Nu   (enable: push N)
	//   \x1b[=0;1u  →  \x1b[<u    (disable: pop 1)
	// Debug: log all sequences written through the translator.
	// Remove this once the keyboard escape issue is fixed.
	logF, _ := os.OpenFile("/tmp/pigeon-kbd-debug.log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	output := &kittyTranslatingWriter{File: os.Stdout, log: logF}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		output.resetKeyboard()
		os.Exit(0)
	}()

	p := tea.NewProgram(m, tea.WithOutput(output))
	_, runErr := p.Run()
	output.resetKeyboard()
	signal.Stop(sigCh)
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "pigeon: runtime error: %v\n", runErr)
		os.Exit(1)
	}
}

// defaultModel returns a sensible default model depending on which providers
// are configured.
func defaultModel() string {
	if key := os.Getenv(app.ZaiAPIKeyEnv); key != "" {
		return "gpt-4o"
	}
	// Check for stored Zai credentials.
	if key, err := auth.GetZaiAPIKey(); err == nil && key != "" {
		return "gpt-4o"
	}
	if key := os.Getenv(app.OpenRouterAPIKeyEnv); key != "" {
		return "openai/gpt-4o-mini"
	}
	// LM Studio default.
	return "qwen3.5-27B"
}

// ─── login / logout sub-commands ────────────────────────────────────────────



// ── kittyTranslatingWriter ────────────────────────────────────────────────────
//
// bubbletea v2 uses the kitty keyboard SET form (\x1b[=flags;1u) to enable
// and disable keyboard enhancements.  WezTerm (and the kitty terminal itself)
// use a STACK-based model where only the PUSH (\x1b[>flags u) and POP
// (\x1b[<n u) forms are tracked; the SET form is silently ignored, so
// bubbletea's cleanup never takes effect and the shell sees garbled arrow keys.
//
// This writer scans the byte stream for the SET sequences and rewrites them:
//   \x1b[=<N>;1u  →  \x1b[><N>u  (enable flags N: PUSH)
//   \x1b[=0;1u    →  \x1b[<u     (disable: POP 1)
//
// All other bytes are forwarded unchanged.
//
// It embeds *os.File so bubbletea can call Fd(), Read(), Close() etc.
// and correctly detect the TTY for size queries and raw-mode setup.
type kittyTranslatingWriter struct {
	*os.File        // Fd() / Read() / Close() — required by bubbletea's term.File check
	buf     []byte  // incomplete escape sequence pending more bytes
	log     *os.File // optional debug log
}

// resetKeyboard sends the full set of keyboard-mode reset sequences directly
// to the TTY.  Called both after p.Run() returns and on SIGTERM/SIGINT.
func (kw *kittyTranslatingWriter) resetKeyboard() {
	// \x1b[>4;0m  explicit modifyOtherKeys disable (level 0)
	// \x1b[<u     pop one entry from kitty keyboard stack
	// \x1b[>u     push 0 (= disable) as final hard reset
	const seqs = "\x1b[>4;0m\x1b[<u\x1b[>u"
	if kw.log != nil {
		fmt.Fprintf(kw.log, "RESET_KEYBOARD: %q\n", seqs)
	}
	if tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err == nil {
		fmt.Fprint(tty, seqs)
		_ = tty.Close()
	} else {
		fmt.Fprint(kw.File, seqs)
	}
}

func (kw *kittyTranslatingWriter) Write(p []byte) (int, error) {
	// Work on buf + p so we handle sequences split across two writes.
	data := append(kw.buf, p...) //nolint:gocritic
	kw.buf = nil

	out := make([]byte, 0, len(data))
	i := 0
	for i < len(data) {
		// Fast path: not an ESC byte.
		if data[i] != 0x1b {
			out = append(out, data[i])
			i++
			continue
		}
		// We have ESC.  Look for the full CSI = N ; 1 u pattern.
		// Minimum: \x1b [ = 0 ; 1 u  = 7 bytes
		rest := data[i:]
		if len(rest) < 2 {
			// Not enough data yet — buffer and wait for next Write.
			kw.buf = rest
			break
		}
		if rest[1] != '[' {
			// Not a CSI sequence — pass ESC through.
			out = append(out, rest[0])
			i++
			continue
		}
		// CSI sequence starting at rest[2].
		// Look for the terminating byte (anything 0x40–0x7e).
		end := -1
		for j := 2; j < len(rest); j++ {
			if rest[j] >= 0x40 && rest[j] <= 0x7e {
				end = j
				break
			}
		}
		if end < 0 {
			// Sequence not yet complete — buffer remainder.
			kw.buf = rest
			break
		}
		seq := rest[:end+1] // full CSI sequence including terminator

		translated := kw.translateKittySet(seq)
		if kw.log != nil {
			if !bytes.Equal(seq, translated) {
				fmt.Fprintf(kw.log, "TRANSLATE: %q -> %q\n", seq, translated)
			} else if bytes.Contains(seq, []byte("u")) || bytes.Contains(seq, []byte(";2m")) {
				// only log keyboard-related sequences to keep log small
				fmt.Fprintf(kw.log, "PASSTHRU:  %q\n", seq)
			}
		}
		out = append(out, translated...)
		i += end + 1
	}

	if len(out) > 0 {
		_, err := kw.File.Write(out)
		if err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// translateKittySet rewrites \x1b[=flags;1u sequences to push/pop form.
// seq is the full CSI sequence including \x1b[.
func (kw *kittyTranslatingWriter) translateKittySet(seq []byte) []byte {
	// Must be:  \x1b [ = <digits> ; 1 u
	// seq[0]=\x1b seq[1]=[ seq[2]=?  terminator=seq[end]='u'
	if len(seq) < 7 {
		return seq
	}
	if seq[2] != '=' || seq[len(seq)-1] != 'u' {
		return seq
	}
	// Extract inner: everything between '=' and 'u', e.g. "0;1" or "1;1"
	inner := seq[3 : len(seq)-1] // e.g. []byte("0;1") or []byte("1;1")
	// Must end with ";1"
	if !bytes.HasSuffix(inner, []byte(";1")) {
		return seq
	}
	flags := inner[:len(inner)-2] // the flag number, e.g. "0" or "1"
	if string(flags) == "0" {
		// Disable: POP 1 from the kitty stack.
		return []byte("\x1b[<u")
	}
	// Enable: PUSH the flags onto the stack.
	return append([]byte("\x1b[>"), append(flags, 'u')...)
}
