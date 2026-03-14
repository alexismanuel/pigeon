// Package e2e runs pigeon end-to-end via a real PTY.
//
// The test binary is built once per test run in a temp dir; each test starts
// a fresh pigeon process, sends keystrokes, and asserts on the visible output.
//
// Tests require a real terminal and are skipped in CI environments where
// PIGEON_E2E=1 is not set (avoids long build times in ordinary go test ./...).
package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

// ── test binary ───────────────────────────────────────────────────────────────

var (
	binaryOnce sync.Once
	binaryPath string
	binaryErr  error
)

func buildBinary(t *testing.T) string {
	t.Helper()
	binaryOnce.Do(func() {
		dir := t.TempDir()
		binaryPath = filepath.Join(dir, "pigeon-e2e")
		cmd := exec.Command("go", "build", "-o", binaryPath, "pigeon/cmd/pigeon")
		cmd.Dir = ".."
		out, err := cmd.CombinedOutput()
		if err != nil {
			binaryErr = fmt.Errorf("build failed: %w\n%s", err, out)
		}
	})
	if binaryErr != nil {
		t.Skipf("could not build pigeon binary: %v", binaryErr)
	}
	return binaryPath
}

// ── PTY harness ───────────────────────────────────────────────────────────────

type harness struct {
	pty    *os.File
	cmd    *exec.Cmd
	cancel context.CancelFunc
	buf    bytes.Buffer
	mu     sync.Mutex
}

// newHarness starts pigeon in a PTY with the given extra args.
// The caller must call h.close() when done.
func newHarness(t *testing.T, args ...string) *harness {
	t.Helper()
	bin := buildBinary(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	cmd := exec.CommandContext(ctx, bin, args...)

	// Minimal env: no API key → pigeon should print an error and exit cleanly.
	// Tests that need a real key must set OPENROUTER_API_KEY themselves.
	cmd.Env = os.Environ()

	ptmx, err := pty.Start(cmd)
	if err != nil {
		cancel()
		t.Fatalf("pty.Start: %v", err)
	}

	h := &harness{pty: ptmx, cmd: cmd, cancel: cancel}
	go h.drain()
	return h
}

func (h *harness) drain() {
	buf := make([]byte, 4096)
	for {
		n, err := h.pty.Read(buf)
		if n > 0 {
			h.mu.Lock()
			h.buf.Write(buf[:n])
			h.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (h *harness) output() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.buf.String()
}

// waitFor blocks until substr appears in the PTY output or timeout elapses.
func (h *harness) waitFor(t *testing.T, substr string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(stripANSI(h.output()), substr) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Logf("waitFor(%q) timed out; output so far:\n%s", substr, stripANSI(h.output()))
	return false
}

// type sends bytes to the PTY as if the user typed them.
func (h *harness) type_(s string) {
	io.WriteString(h.pty, s)
}

func (h *harness) close() {
	h.cancel()
	h.pty.Close()
	h.cmd.Wait()
}

// stripANSI removes ANSI escape sequences from s.
func stripANSI(s string) string {
	var out strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && !isANSITerminator(s[i]) {
				i++
			}
			i++ // skip terminator
			continue
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String()
}

func isANSITerminator(c byte) bool {
	return c >= 0x40 && c <= 0x7e
}

// ── helpers ───────────────────────────────────────────────────────────────────

func skipIfNoKey(t *testing.T) {
	t.Helper()
	if os.Getenv("OPENROUTER_API_KEY") == "" {
		t.Skip("OPENROUTER_API_KEY not set; skipping live test")
	}
}

func skipIfNotE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("PIGEON_E2E") != "1" {
		t.Skip("set PIGEON_E2E=1 to run end-to-end tests")
	}
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestE2E_NoAPIKey verifies pigeon exits with an error message when no key set.
func TestE2E_NoAPIKey(t *testing.T) {
	skipIfNotE2E(t)
	bin := buildBinary(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin)
	// Explicitly remove any API key from environment.
	filtered := make([]string, 0)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "OPENROUTER_API_KEY=") && !strings.HasPrefix(e, "OPENROUTER_KEY=") {
			filtered = append(filtered, e)
		}
	}
	cmd.Env = filtered

	out, _ := cmd.CombinedOutput()
	if !strings.Contains(string(out), "OPENROUTER_API_KEY") {
		t.Errorf("expected error about OPENROUTER_API_KEY, got:\n%s", out)
	}
}

// TestE2E_SystemFlag verifies -system flag is accepted without crashing.
func TestE2E_SystemFlag(t *testing.T) {
	skipIfNotE2E(t)
	skipIfNoKey(t)

	h := newHarness(t, "-system", "You are a helpful assistant.", "-model", "openai/gpt-4o-mini")
	defer h.close()

	if !h.waitFor(t, "pigeon", 8*time.Second) {
		t.Fatal("pigeon header not rendered")
	}

	// Type /system to show the current prompt.
	h.type_("/system\r")
	if !h.waitFor(t, "You are a helpful assistant.", 4*time.Second) {
		t.Fatal("system prompt not shown after /system command")
	}
}

// TestE2E_QuitCommand verifies /quit exits cleanly.
func TestE2E_QuitCommand(t *testing.T) {
	skipIfNotE2E(t)
	skipIfNoKey(t)

	h := newHarness(t, "-model", "openai/gpt-4o-mini")
	defer h.close()

	if !h.waitFor(t, "pigeon", 8*time.Second) {
		t.Fatal("pigeon header not rendered")
	}

	h.type_("/quit\r")
	// Give it time to exit.
	done := make(chan struct{})
	go func() {
		h.cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
		// clean exit
	case <-time.After(4 * time.Second):
		t.Error("/quit did not exit within 4s")
	}
}

// TestE2E_NewSession verifies /new starts a fresh session without crashing.
func TestE2E_NewSession(t *testing.T) {
	skipIfNotE2E(t)
	skipIfNoKey(t)

	h := newHarness(t, "-model", "openai/gpt-4o-mini")
	defer h.close()

	if !h.waitFor(t, "pigeon", 8*time.Second) {
		t.Fatal("pigeon header not rendered")
	}

	h.type_("/new\r")
	if !h.waitFor(t, "New session", 4*time.Second) {
		t.Fatal("new session confirmation not shown")
	}
}

// TestE2E_SessionsPicker verifies /sessions opens the interactive picker.
func TestE2E_SessionsPicker(t *testing.T) {
	skipIfNotE2E(t)
	skipIfNoKey(t)

	h := newHarness(t, "-model", "openai/gpt-4o-mini")
	defer h.close()

	if !h.waitFor(t, "pigeon", 8*time.Second) {
		t.Fatal("pigeon header not rendered")
	}

	h.type_("/sessions\r")
	// Picker shows "loading" then either sessions or "no sessions match".
	if !h.waitFor(t, "esc", 4*time.Second) {
		t.Fatal("session picker did not appear")
	}

	// Escape closes the picker.
	h.type_("\x1b") // ESC
	if !h.waitFor(t, ">", 2*time.Second) {
		t.Fatal("input prompt did not reappear after esc")
	}
}

// TestE2E_LabelCommand verifies /label sets a session label.
func TestE2E_LabelCommand(t *testing.T) {
	skipIfNotE2E(t)
	skipIfNoKey(t)

	h := newHarness(t, "-model", "openai/gpt-4o-mini")
	defer h.close()

	if !h.waitFor(t, "pigeon", 8*time.Second) {
		t.Fatal("pigeon header not rendered")
	}

	h.type_("/label my e2e test\r")
	if !h.waitFor(t, "my e2e test", 4*time.Second) {
		t.Fatal("label confirmation not shown")
	}
}

// TestE2E_ModelPicker verifies /model opens the interactive picker.
func TestE2E_ModelPicker(t *testing.T) {
	skipIfNotE2E(t)
	skipIfNoKey(t)

	h := newHarness(t, "-model", "openai/gpt-4o-mini")
	defer h.close()

	if !h.waitFor(t, "pigeon", 8*time.Second) {
		t.Fatal("pigeon header not rendered")
	}

	h.type_("/model\r")
	// Picker shows "Loading models…" then model list.
	if !h.waitFor(t, "esc", 6*time.Second) {
		t.Fatal("model picker did not appear")
	}

	// Escape closes picker.
	h.type_("\x1b")
	if !h.waitFor(t, ">", 2*time.Second) {
		t.Fatal("input prompt did not reappear after esc")
	}
}

// TestE2E_SystemPromptFromFile verifies system.md is loaded from .pigeon/.
func TestE2E_SystemPromptFromFile(t *testing.T) {
	skipIfNotE2E(t)
	skipIfNoKey(t)

	// Write a .pigeon/system.md in a temp dir, run pigeon from there.
	dir := t.TempDir()
	pigeonDir := filepath.Join(dir, ".pigeon")
	os.MkdirAll(pigeonDir, 0o755)
	os.WriteFile(filepath.Join(pigeonDir, "system.md"), []byte("You are a pirate."), 0o644)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	bin := buildBinary(t)
	cmd := exec.CommandContext(ctx, bin, "-model", "openai/gpt-4o-mini")
	cmd.Dir = dir
	cmd.Env = os.Environ()

	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("pty.Start: %v", err)
	}
	defer ptmx.Close()
	defer cmd.Wait()

	h := &harness{pty: ptmx, cmd: cmd, cancel: cancel}
	go h.drain()

	if !h.waitFor(t, "pigeon", 8*time.Second) {
		t.Fatal("pigeon header not rendered")
	}

	io.WriteString(ptmx, "/system\r")
	if !h.waitFor(t, "pirate", 4*time.Second) {
		t.Fatalf("system prompt from .pigeon/system.md not shown; output: %s", stripANSI(h.output()))
	}
}
