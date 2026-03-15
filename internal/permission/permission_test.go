package permission

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func newTestService() *permissionService {
	return NewService("/tmp", false, []string{}, nil).(*permissionService)
}

func basicReq(session, tool, action string) CreatePermissionRequest {
	return CreatePermissionRequest{
		SessionID:   session,
		ToolName:    tool,
		Action:      action,
		Description: "test",
		Path:        "/tmp/test.txt",
	}
}

// respondAsync reads the first request off the service's subscribe channel and
// calls the given respond function.  Runs in a separate goroutine so it doesn't
// block the test.
func respondAsync(s Service, respond func(id string)) {
	go func() {
		req := <-s.Subscribe()
		respond(req.ID)
	}()
}

// ── skip mode ─────────────────────────────────────────────────────────────────

func TestSkipMode_AutoApprovesAll(t *testing.T) {
	s := NewService("/tmp", true, nil, nil)
	ok, err := s.Request(context.Background(), basicReq("s1", "bash", "execute"))
	if !ok || err != nil {
		t.Fatalf("skip mode should auto-approve: ok=%v err=%v", ok, err)
	}
}

// ── allowlist ─────────────────────────────────────────────────────────────────

func TestAllowlist_ToolName(t *testing.T) {
	s := NewService("/tmp", false, []string{"bash", "read"}, nil)
	ok, err := s.Request(context.Background(), basicReq("s1", "bash", "execute"))
	if !ok || err != nil {
		t.Fatalf("allowlisted tool should be auto-approved: ok=%v err=%v", ok, err)
	}
}

func TestAllowlist_ToolNameAction(t *testing.T) {
	s := NewService("/tmp", false, []string{"bash:execute"}, nil)
	ok, err := s.Request(context.Background(), basicReq("s1", "bash", "execute"))
	if !ok || err != nil {
		t.Fatalf("allowlisted tool:action should be auto-approved: ok=%v err=%v", ok, err)
	}
}

func TestAllowlist_MismatchedAction(t *testing.T) {
	s := NewService("/tmp", false, []string{"bash:read"}, nil)
	// "bash:execute" is NOT in the allowlist.
	respondAsync(s, func(id string) { s.Deny(id) })
	ok, err := s.Request(context.Background(), basicReq("s1", "bash", "execute"))
	if ok {
		t.Fatal("mismatched action should NOT be auto-approved")
	}
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("expected ErrDenied, got %v", err)
	}
}

func TestAllowlist_EmptyAllowlist(t *testing.T) {
	s := NewService("/tmp", false, nil, nil)
	respondAsync(s, func(id string) { s.Grant(id) })
	ok, err := s.Request(context.Background(), basicReq("s1", "bash", "execute"))
	if !ok || err != nil {
		t.Fatalf("expected grant: ok=%v err=%v", ok, err)
	}
}

// ── grant / deny ──────────────────────────────────────────────────────────────

func TestGrant_OneTime(t *testing.T) {
	s := NewService("/tmp", false, nil, nil)
	respondAsync(s, func(id string) { s.Grant(id) })
	ok, err := s.Request(context.Background(), basicReq("s1", "bash", "execute"))
	if !ok || err != nil {
		t.Fatalf("expected one-time grant: ok=%v err=%v", ok, err)
	}
}

func TestDeny_ReturnsFalseAndErrDenied(t *testing.T) {
	s := NewService("/tmp", false, nil, nil)
	respondAsync(s, func(id string) { s.Deny(id) })
	ok, err := s.Request(context.Background(), basicReq("s1", "bash", "execute"))
	if ok {
		t.Fatal("denied request should return false")
	}
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("expected ErrDenied, got %v", err)
	}
}

// ── persistent grants ─────────────────────────────────────────────────────────

func TestGrantPersistent_CachesForSession(t *testing.T) {
	s := NewService("/tmp", false, nil, nil)

	// First request: user grants persistently.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		req := <-s.Subscribe()
		s.GrantPersistent(req.ID)
	}()
	ok, err := s.Request(context.Background(), basicReq("s1", "bash", "execute"))
	wg.Wait()
	if !ok || err != nil {
		t.Fatalf("first request should be granted: ok=%v err=%v", ok, err)
	}

	// Second identical request: should be auto-approved from cache.
	ok, err = s.Request(context.Background(), basicReq("s1", "bash", "execute"))
	if !ok || err != nil {
		t.Fatalf("second request should be auto-approved via cache: ok=%v err=%v", ok, err)
	}
}

func TestGrantPersistent_DifferentSessionNotCached(t *testing.T) {
	s := NewService("/tmp", false, nil, nil)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		req := <-s.Subscribe()
		s.GrantPersistent(req.ID)
	}()
	s.Request(context.Background(), basicReq("session-A", "bash", "execute")) //nolint
	wg.Wait()

	// Different session should still need permission.
	respondAsync(s, func(id string) { s.Grant(id) })
	ok, err := s.Request(context.Background(), basicReq("session-B", "bash", "execute"))
	if !ok || err != nil {
		t.Fatalf("different session should prompt: ok=%v err=%v", ok, err)
	}
}

func TestGrantPersistent_DifferentActionNotCached(t *testing.T) {
	s := NewService("/tmp", false, nil, nil)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		req := <-s.Subscribe()
		s.GrantPersistent(req.ID)
	}()
	s.Request(context.Background(), basicReq("s1", "bash", "execute")) //nolint
	wg.Wait()

	// Same tool but different action should NOT be cached.
	respondAsync(s, func(id string) { s.Grant(id) })
	ok, err := s.Request(context.Background(), basicReq("s1", "bash", "read"))
	if !ok || err != nil {
		t.Fatalf("different action should prompt: ok=%v err=%v", ok, err)
	}
}

// ── session auto-approval ─────────────────────────────────────────────────────

func TestAutoApproveSession_SkipsAllPrompts(t *testing.T) {
	s := NewService("/tmp", false, nil, nil)
	s.AutoApproveSession("trusted-session")

	ok, err := s.Request(context.Background(), basicReq("trusted-session", "bash", "execute"))
	if !ok || err != nil {
		t.Fatalf("auto-approved session should always pass: ok=%v err=%v", ok, err)
	}
	ok, err = s.Request(context.Background(), basicReq("trusted-session", "write", "create"))
	if !ok || err != nil {
		t.Fatalf("second auto-approved request failed: ok=%v err=%v", ok, err)
	}
}

func TestAutoApproveSession_DoesNotAffectOtherSessions(t *testing.T) {
	s := NewService("/tmp", false, nil, nil)
	s.AutoApproveSession("trusted")

	respondAsync(s, func(id string) { s.Deny(id) })
	ok, _ := s.Request(context.Background(), basicReq("untrusted", "bash", "execute"))
	if ok {
		t.Fatal("untrusted session should still require permission")
	}
}

// ── context cancellation ──────────────────────────────────────────────────────

func TestRequest_ContextCancelledWhileWaiting(t *testing.T) {
	s := NewService("/tmp", false, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Consume the published request but don't respond — instead cancel the context.
		<-s.Subscribe()
		cancel()
	}()

	ok, err := s.Request(ctx, basicReq("s1", "bash", "execute"))
	wg.Wait()
	if ok {
		t.Fatal("cancelled context should not grant permission")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// ── context key helpers ───────────────────────────────────────────────────────

func TestContextWithSessionID_RoundTrip(t *testing.T) {
	ctx := ContextWithSessionID(context.Background(), "my-session")
	if got := SessionIDFromContext(ctx); got != "my-session" {
		t.Errorf("expected 'my-session', got %q", got)
	}
}

func TestSessionIDFromContext_MissingKey(t *testing.T) {
	if got := SessionIDFromContext(context.Background()); got != "" {
		t.Errorf("expected empty string for missing key, got %q", got)
	}
}

// ── bash deny patterns ────────────────────────────────────────────────────────

func TestBashDenyPatterns_SpaceStar(t *testing.T) {
	s := NewService("/tmp", false, nil, []string{"rm *", "chown *"})
	ok, err := s.Request(context.Background(), CreatePermissionRequest{
		SessionID: "s1", ToolName: "bash", Action: "execute",
		Path:   "/tmp",
		Params: BashParams{Command: "rm -rf /tmp/junk"},
	})
	if ok {
		t.Fatal("rm command should be auto-denied")
	}
	if err == nil {
		t.Fatal("expected error for denied command")
	}
}

func TestBashDenyPatterns_ExactPrefix(t *testing.T) {
	s := NewService("/tmp", false, nil, []string{"git push"})
	ok, _ := s.Request(context.Background(), CreatePermissionRequest{
		SessionID: "s1", ToolName: "bash", Action: "execute",
		Path:   "/tmp",
		Params: BashParams{Command: "git push origin main"},
	})
	if ok {
		t.Fatal("git push should be auto-denied")
	}
}

func TestBashDenyPatterns_ExactMatch(t *testing.T) {
	s := NewService("/tmp", false, nil, []string{"git push"})
	ok, _ := s.Request(context.Background(), CreatePermissionRequest{
		SessionID: "s1", ToolName: "bash", Action: "execute",
		Path:   "/tmp",
		Params: BashParams{Command: "git push"},
	})
	if ok {
		t.Fatal("bare git push should be auto-denied")
	}
}

func TestBashDenyPatterns_NonMatchingCommandPrompts(t *testing.T) {
	s := NewService("/tmp", false, nil, []string{"rm *"})
	respondAsync(s, func(id string) { s.Grant(id) })
	ok, err := s.Request(context.Background(), CreatePermissionRequest{
		SessionID: "s1", ToolName: "bash", Action: "execute",
		Path:   "/tmp",
		Params: BashParams{Command: "ls -la"},
	})
	if !ok || err != nil {
		t.Fatalf("non-matching command should prompt and be granted: ok=%v err=%v", ok, err)
	}
}

func TestBashDenyPatterns_NoBashParamsSkipsCheck(t *testing.T) {
	// If the tool is bash but Params is not BashParams, the pattern check is skipped.
	s := NewService("/tmp", false, nil, []string{"rm *"})
	respondAsync(s, func(id string) { s.Grant(id) })
	ok, err := s.Request(context.Background(), CreatePermissionRequest{
		SessionID: "s1", ToolName: "bash", Action: "execute",
		Path: "/tmp",
		// no Params
	})
	if !ok || err != nil {
		t.Fatalf("missing params should not trigger deny: ok=%v err=%v", ok, err)
	}
}

func TestBashDenyPatterns_DenyBeforeAllowlist(t *testing.T) {
	// Deny wins even when the tool is in the allowlist.
	s := NewService("/tmp", false, []string{"bash"}, []string{"rm *"})
	ok, _ := s.Request(context.Background(), CreatePermissionRequest{
		SessionID: "s1", ToolName: "bash", Action: "execute",
		Path:   "/tmp",
		Params: BashParams{Command: "rm /important/file"},
	})
	if ok {
		t.Fatal("deny pattern should beat allowlist")
	}
}

func TestMatchesDenyPattern(t *testing.T) {
	tests := []struct {
		pattern string
		cmd     string
		want    bool
	}{
		{"rm *", "rm -rf /", true},
		{"rm *", "rm file.txt", true},
		{"rm *", "rm", true}, // bare command matches "rm " prefix — see rule
		{"rm *", "chmod 755 x", false},
		{"chown *", "chown root:root /etc/passwd", true},
		{"git push", "git push", true},
		{"git push", "git push origin main", true},
		{"git push", "git pull", false},
		{"git push", "git pushall", false}, // "git pushall" doesn't start with "git push "
		{"sudo*", "sudo rm -rf /", true},
		{"sudo*", "echo sudo", false},
	}
	for _, tt := range tests {
		got := matchesDenyPattern(tt.cmd, []string{tt.pattern}) != ""
		if got != tt.want {
			t.Errorf("matchesDenyPattern(%q, %q) = %v, want %v", tt.cmd, tt.pattern, got, tt.want)
		}
	}
}

// ── resolveDir ────────────────────────────────────────────────────────────────

func TestResolveDir_EmptyPath(t *testing.T) {
	s := newTestService()
	if got := s.resolveDir(""); got != "/tmp" {
		t.Errorf("empty path should resolve to workingDir, got %q", got)
	}
}

func TestResolveDir_DotPath(t *testing.T) {
	s := newTestService()
	if got := s.resolveDir("."); got != "/tmp" {
		t.Errorf("'.' path should resolve to workingDir, got %q", got)
	}
}

func TestResolveDir_ExistingDir(t *testing.T) {
	s := newTestService()
	got := s.resolveDir("/tmp")
	if got != "/tmp" {
		t.Errorf("existing dir should return itself, got %q", got)
	}
}

func TestResolveDir_ExistingFile(t *testing.T) {
	s := newTestService()
	// /etc/hosts is a file — should return /etc
	got := s.resolveDir("/etc/hosts")
	if got != "/etc" {
		t.Errorf("file path should return parent dir, got %q", got)
	}
}

func TestResolveDir_NonexistentFile(t *testing.T) {
	s := newTestService()
	got := s.resolveDir("/nonexistent/path/file.txt")
	if got != "/nonexistent/path" {
		t.Errorf("non-existent file should return parent dir, got %q", got)
	}
}

// ── sequential requests ───────────────────────────────────────────────────────

func TestSequentialRequests_GrantThenDeny(t *testing.T) {
	s := NewService("/tmp", false, nil, nil)

	// First: grant
	respondAsync(s, func(id string) { s.Grant(id) })
	ok1, _ := s.Request(context.Background(), basicReq("s1", "bash", "execute"))
	if !ok1 {
		t.Fatal("first request should be granted")
	}

	// Second: deny (no persistent cache, so prompts again)
	respondAsync(s, func(id string) { s.Deny(id) })
	ok2, err2 := s.Request(context.Background(), basicReq("s1", "bash", "execute"))
	if ok2 {
		t.Fatal("second request should be denied")
	}
	if !errors.Is(err2, ErrDenied) {
		t.Fatalf("expected ErrDenied, got %v", err2)
	}
}
