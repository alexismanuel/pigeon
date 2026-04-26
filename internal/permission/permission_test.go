package permission

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func newTestService() *permissionService {
	return NewService("/tmp", false, []string{}, nil, false).(*permissionService)
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
	s := NewService("/tmp", true, nil, nil, false)
	ok, err := s.Request(context.Background(), basicReq("s1", "bash", "execute"))
	if !ok || err != nil {
		t.Fatalf("skip mode should auto-approve: ok=%v err=%v", ok, err)
	}
}

// ── allowlist ─────────────────────────────────────────────────────────────────

func TestAllowlist_ToolName(t *testing.T) {
	s := NewService("/tmp", false, []string{"bash", "read"}, nil, false)
	ok, err := s.Request(context.Background(), basicReq("s1", "bash", "execute"))
	if !ok || err != nil {
		t.Fatalf("allowlisted tool should be auto-approved: ok=%v err=%v", ok, err)
	}
}

func TestAllowlist_ToolNameAction(t *testing.T) {
	s := NewService("/tmp", false, []string{"bash:execute"}, nil, false)
	ok, err := s.Request(context.Background(), basicReq("s1", "bash", "execute"))
	if !ok || err != nil {
		t.Fatalf("allowlisted tool:action should be auto-approved: ok=%v err=%v", ok, err)
	}
}

func TestAllowlist_MismatchedAction(t *testing.T) {
	s := NewService("/tmp", false, []string{"bash:read"}, nil, false)
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
	s := NewService("/tmp", false, nil, nil, false)
	respondAsync(s, func(id string) { s.Grant(id) })
	ok, err := s.Request(context.Background(), basicReq("s1", "bash", "execute"))
	if !ok || err != nil {
		t.Fatalf("expected grant: ok=%v err=%v", ok, err)
	}
}

// ── grant / deny ──────────────────────────────────────────────────────────────

func TestGrant_OneTime(t *testing.T) {
	s := NewService("/tmp", false, nil, nil, false)
	respondAsync(s, func(id string) { s.Grant(id) })
	ok, err := s.Request(context.Background(), basicReq("s1", "bash", "execute"))
	if !ok || err != nil {
		t.Fatalf("expected one-time grant: ok=%v err=%v", ok, err)
	}
}

func TestDeny_ReturnsFalseAndErrDenied(t *testing.T) {
	s := NewService("/tmp", false, nil, nil, false)
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
	s := NewService("/tmp", false, nil, nil, false)

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
	s := NewService("/tmp", false, nil, nil, false)

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
	s := NewService("/tmp", false, nil, nil, false)

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
	s := NewService("/tmp", false, nil, nil, false)
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
	s := NewService("/tmp", false, nil, nil, false)
	s.AutoApproveSession("trusted")

	respondAsync(s, func(id string) { s.Deny(id) })
	ok, _ := s.Request(context.Background(), basicReq("untrusted", "bash", "execute"))
	if ok {
		t.Fatal("untrusted session should still require permission")
	}
}

// ── context cancellation ──────────────────────────────────────────────────────

func TestRequest_ContextCancelledWhileWaiting(t *testing.T) {
	s := NewService("/tmp", false, nil, nil, false)

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
	s := NewService("/tmp", false, nil, []string{"rm *", "chown *"}, false)
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
	s := NewService("/tmp", false, nil, []string{"git push"}, false)
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
	s := NewService("/tmp", false, nil, []string{"git push"}, false)
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
	s := NewService("/tmp", false, nil, []string{"rm *"}, false)
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
	s := NewService("/tmp", false, nil, []string{"rm *"}, false)
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
	s := NewService("/tmp", false, []string{"bash"}, []string{"rm *"}, false)
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
	s := NewService("/tmp", false, nil, nil, false)

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

// ── sandbox mode ────────────────────────────────────────────────────────────

func TestSandboxMode_WithinWorkingDir_AllowedByAllowlist(t *testing.T) {
	// Even with sandbox mode, paths inside the working dir are auto-approved
	// when the tool is in the allowlist.
	s := NewService("/tmp", false, []string{"write"}, nil, true)
	ok, err := s.Request(context.Background(), CreatePermissionRequest{
		SessionID: "s1", ToolName: "write", Action: "create",
		Path: "/tmp/subdir/file.txt",
	})
	if !ok || err != nil {
		t.Fatalf("write inside working dir should be auto-approved: ok=%v err=%v", ok, err)
	}
}

func TestSandboxMode_OutsideWorkingDir_PromptsEvenWithAllowlist(t *testing.T) {
	// With sandbox mode on, writing outside working dir must prompt,
	// even if "write" is in the allowlist.
	s := NewService("/tmp", false, []string{"write"}, nil, true)
	respondAsync(s, func(id string) { s.Grant(id) })
	ok, err := s.Request(context.Background(), CreatePermissionRequest{
		SessionID: "s1", ToolName: "write", Action: "create",
		Path: "/var/log/app.log",
	})
	if !ok || err != nil {
		t.Fatalf("outside-sandbox write should prompt and be granted: ok=%v err=%v", ok, err)
	}
}

func TestSandboxMode_OutsideWorkingDir_Denied(t *testing.T) {
	s := NewService("/tmp", false, []string{"write"}, nil, true)
	respondAsync(s, func(id string) { s.Deny(id) })
	ok, err := s.Request(context.Background(), CreatePermissionRequest{
		SessionID: "s1", ToolName: "write", Action: "create",
		Path: "/var/log/app.log",
	})
	if ok {
		t.Fatal("denied out-of-sandbox write should return false")
	}
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("expected ErrDenied, got %v", err)
	}
}

func TestSandboxMode_EditOutsideSandbox_Prompts(t *testing.T) {
	s := NewService("/tmp", false, []string{"edit"}, nil, true)
	respondAsync(s, func(id string) { s.Deny(id) })
	ok, _ := s.Request(context.Background(), CreatePermissionRequest{
		SessionID: "s1", ToolName: "edit", Action: "modify",
		Path: "/etc/hosts",
	})
	if ok {
		t.Fatal("edit outside sandbox should not be auto-approved")
	}
}

func TestSandboxMode_EditInsideSandbox_AutoApproved(t *testing.T) {
	s := NewService("/tmp", false, []string{"edit"}, nil, true)
	ok, err := s.Request(context.Background(), CreatePermissionRequest{
		SessionID: "s1", ToolName: "edit", Action: "modify",
		Path: "/tmp/myfile.go",
	})
	if !ok || err != nil {
		t.Fatalf("edit inside sandbox should be auto-approved: ok=%v err=%v", ok, err)
	}
}

func TestSandboxMode_BashNotAffected(t *testing.T) {
	// Bash commands are not sandboxed — they always respect the allowlist.
	s := NewService("/tmp", false, []string{"bash"}, nil, true)
	ok, err := s.Request(context.Background(), CreatePermissionRequest{
		SessionID: "s1", ToolName: "bash", Action: "execute",
		Path:   "/tmp",
		Params: BashParams{Command: "cat /etc/passwd"},
	})
	if !ok || err != nil {
		t.Fatalf("bash should not be sandboxed: ok=%v err=%v", ok, err)
	}
}

func TestSandboxMode_SandboxOff_AllowlistWorksEverywhere(t *testing.T) {
	// Without sandbox mode, the allowlist works for all paths.
	s := NewService("/tmp", false, []string{"write"}, nil, false)
	ok, err := s.Request(context.Background(), CreatePermissionRequest{
		SessionID: "s1", ToolName: "write", Action: "create",
		Path: "/var/log/app.log",
	})
	if !ok || err != nil {
		t.Fatalf("without sandbox, allowlist should work everywhere: ok=%v err=%v", ok, err)
	}
}

func TestSandboxMode_PersistentGrantOutOfSandbox(t *testing.T) {
	// Granting persistently for an out-of-sandbox path should cache it.
	s := NewService("/tmp", false, []string{"write"}, nil, true)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		req := <-s.Subscribe()
		s.GrantPersistent(req.ID)
	}()
	ok, err := s.Request(context.Background(), CreatePermissionRequest{
		SessionID: "s1", ToolName: "write", Action: "create",
		Path: "/var/log/app.log",
	})
	wg.Wait()
	if !ok || err != nil {
		t.Fatalf("first out-of-sandbox request should be granted: ok=%v err=%v", ok, err)
	}

	// Second identical request should be auto-approved via cache.
	ok, err = s.Request(context.Background(), CreatePermissionRequest{
		SessionID: "s1", ToolName: "write", Action: "create",
		Path: "/var/log/app.log",
	})
	if !ok || err != nil {
		t.Fatalf("cached out-of-sandbox request should be auto-approved: ok=%v err=%v", ok, err)
	}
}

// ── isWithinSandbox ─────────────────────────────────────────────────────────

func TestIsWithinSandbox(t *testing.T) {
	dirs := []string{"/tmp", "/home/user/.config/pigeon", "/home/user/.pigeon"}

	tests := []struct {
		path string
		want bool
	}{
		{"/tmp", true},
		{"/tmp/", true},
		{"/tmp/subdir/file.txt", true},
		{"/var/log", false},
		{"/home/user/.config/pigeon/settings.json", true},
		{"/home/user/.config/pigeon/extensions/hello.lua", true},
		{"/home/user/.pigeon/sessions/2024-01-01-abcd.jsonl", true},
		{"/home/user/.config", false},
		{"/home/user/.pigeonz", false},
		{"/tmpfile", false},
	}

	for _, tt := range tests {
		got := isWithinSandbox(tt.path, dirs)
		if got != tt.want {
			t.Errorf("isWithinSandbox(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestComputeSandboxDirs(t *testing.T) {
	dirs := computeSandboxDirs("/tmp")
	if len(dirs) < 3 {
		t.Fatalf("expected at least 3 sandbox dirs, got %d: %v", len(dirs), dirs)
	}
	// First should always be the working dir.
	if dirs[0] != "/tmp" {
		t.Errorf("first sandbox dir should be working dir, got %q", dirs[0])
	}
	// Should contain .pigeon under working dir.
	found := false
	for _, d := range dirs {
		if d == "/tmp/.pigeon" {
			found = true
		}
	}
	if !found {
		t.Error("sandbox dirs should include /tmp/.pigeon")
	}
}
