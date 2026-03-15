// Package permission implements a permission service that intercepts tool calls
// and prompts the user for approval before allowing sensitive operations.
//
// The design follows the same pattern used by Charm's Crush agent:
//   - A Service interface that tools call via Request()
//   - Request() blocks until the user approves or denies (or the context is
//     cancelled)
//   - Multiple grant levels: one-time, session-scoped (remember this exact
//     tool+action+path), and full session auto-approval
//   - A Subscribe() channel that the TUI reads to surface permission dialogs
package permission

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// ErrDenied is returned by Request when the user explicitly denies the
// permission request (as opposed to a context cancellation).
var ErrDenied = errors.New("user denied permission")

// ── context helpers ───────────────────────────────────────────────────────────

type contextKey string

const sessionIDKey contextKey = "sessionID"

// ContextWithSessionID returns a copy of ctx that carries the given session ID.
// Tools read it back via SessionIDFromContext so the permission service can
// associate requests with the correct conversation.
func ContextWithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, sessionIDKey, sessionID)
}

// SessionIDFromContext returns the session ID stored in ctx, or "" if none.
func SessionIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(sessionIDKey).(string)
	return id
}

// ── params structs ────────────────────────────────────────────────────────────

// BashParams carries the details of a bash tool call shown in the dialog.
type BashParams struct {
	Command string
}

// WriteParams carries the details of a write tool call shown in the dialog.
type WriteParams struct {
	Path    string
	Content string
}

// EditParams carries the details of an edit tool call shown in the dialog.
type EditParams struct {
	Path    string
	OldText string
	NewText string
}

// ── request types ─────────────────────────────────────────────────────────────

// CreatePermissionRequest is what a tool passes to Service.Request.
type CreatePermissionRequest struct {
	SessionID   string
	ToolName    string
	Action      string
	Description string
	Path        string
	Params      any
}

// Request is the internal representation the service creates before surfacing
// the request to the TUI.
type Request struct {
	ID          string
	SessionID   string
	ToolName    string
	Action      string
	Description string
	Path        string
	Params      any
}

// ── service interface ─────────────────────────────────────────────────────────

// Service manages permission checks for tool calls.
type Service interface {
	// Request checks whether the given tool call is permitted.
	// It blocks the calling goroutine until the user responds or ctx is
	// cancelled.  Returns (true, nil) if allowed, (false, ErrDenied) if
	// denied, or (false, ctx.Err()) on cancellation.
	Request(ctx context.Context, opts CreatePermissionRequest) (bool, error)

	// Grant approves the pending request with the given ID once.
	Grant(id string)

	// GrantPersistent approves the pending request and caches it so that
	// future requests with the same tool, action, path, and session are
	// automatically approved for the rest of the session.
	GrantPersistent(id string)

	// AutoApproveSession marks the given session so that all future
	// permission requests for that session are auto-approved without
	// prompting the user.
	AutoApproveSession(sessionID string)

	// Deny rejects the pending request with the given ID.
	Deny(id string)

	// Subscribe returns the channel on which pending requests are published.
	// The TUI reads from this channel and must call Grant/GrantPersistent/Deny
	// after the user responds.
	Subscribe() <-chan Request
}

// ── implementation ────────────────────────────────────────────────────────────

type pendingEntry struct {
	respCh  chan bool
	request Request
}

type permissionService struct {
	workingDir       string
	skip             bool
	allowedTools     []string
	bashDenyPatterns []string

	// requestCh carries pending requests to the TUI; buffered so the service
	// never blocks on a slow subscriber.
	requestCh chan Request

	pendingMu sync.Mutex
	pending   map[string]pendingEntry // id → entry waiting for a response

	sessionPermsMu sync.RWMutex
	sessionPerms   []Request // persistent session-scoped grants

	autoSessionsMu sync.RWMutex
	autoSessions   map[string]bool // sessionID → auto-approve all

	// requestMu ensures only one interactive prompt is active at a time,
	// serialising concurrent tool calls that need user input.
	requestMu sync.Mutex
}

// NewService creates a new permission service.
//
//   - workingDir is the base directory used when a path resolves to ".".
//   - skip disables all checks (auto-approve everything).
//   - allowedTools is a list of "toolName" or "toolName:action" strings that
//     are auto-approved without prompting.
//   - bashDenyPatterns is a list of glob patterns matched against bash commands.
//     Matching commands are auto-denied without prompting.
func NewService(workingDir string, skip bool, allowedTools []string, bashDenyPatterns []string) Service {
	return &permissionService{
		workingDir:       workingDir,
		skip:             skip,
		allowedTools:     allowedTools,
		bashDenyPatterns: bashDenyPatterns,
		requestCh:        make(chan Request, 1),
		pending:          make(map[string]pendingEntry),
		autoSessions:     make(map[string]bool),
	}
}

func (s *permissionService) Subscribe() <-chan Request {
	return s.requestCh
}

func (s *permissionService) Request(ctx context.Context, opts CreatePermissionRequest) (bool, error) {
	// ── 1. skip mode ──────────────────────────────────────────────────────────
	if s.skip {
		return true, nil
	}

	// ── 2. bash deny patterns ─────────────────────────────────────────────────
	// Checked before the allowlist: a deny pattern always wins.
	if opts.ToolName == "bash" && len(s.bashDenyPatterns) > 0 {
		if params, ok := opts.Params.(BashParams); ok {
			if pattern := matchesDenyPattern(params.Command, s.bashDenyPatterns); pattern != "" {
				return false, fmt.Errorf("%w: command matches deny pattern %q", ErrDenied, pattern)
			}
		}
	}

	// ── 3. allowlist ──────────────────────────────────────────────────────────
	toolKey := opts.ToolName + ":" + opts.Action
	if slices.Contains(s.allowedTools, opts.ToolName) || slices.Contains(s.allowedTools, toolKey) {
		return true, nil
	}

	// ── 4. session-level auto-approval ────────────────────────────────────────
	s.autoSessionsMu.RLock()
	autoApprove := s.autoSessions[opts.SessionID]
	s.autoSessionsMu.RUnlock()
	if autoApprove {
		return true, nil
	}

	// ── 4. persistent session cache ───────────────────────────────────────────
	dir := s.resolveDir(opts.Path)
	s.sessionPermsMu.RLock()
	for _, p := range s.sessionPerms {
		if p.SessionID == opts.SessionID &&
			p.ToolName == opts.ToolName &&
			p.Action == opts.Action &&
			p.Path == dir {
			s.sessionPermsMu.RUnlock()
			return true, nil
		}
	}
	s.sessionPermsMu.RUnlock()

	// ── 5. interactive prompt ─────────────────────────────────────────────────
	// Serialise so only one dialog is shown at a time.
	s.requestMu.Lock()
	defer s.requestMu.Unlock()

	// Re-check auto-approval in case it was set while we were waiting for the lock.
	s.autoSessionsMu.RLock()
	autoApprove = s.autoSessions[opts.SessionID]
	s.autoSessionsMu.RUnlock()
	if autoApprove {
		return true, nil
	}

	req := Request{
		ID:          newID(),
		SessionID:   opts.SessionID,
		ToolName:    opts.ToolName,
		Action:      opts.Action,
		Description: opts.Description,
		Path:        dir,
		Params:      opts.Params,
	}

	respCh := make(chan bool, 1)
	s.pendingMu.Lock()
	s.pending[req.ID] = pendingEntry{respCh: respCh, request: req}
	s.pendingMu.Unlock()

	defer func() {
		s.pendingMu.Lock()
		delete(s.pending, req.ID)
		s.pendingMu.Unlock()
	}()

	// Publish request to the TUI subscriber.
	select {
	case s.requestCh <- req:
	case <-ctx.Done():
		return false, ctx.Err()
	}

	// Wait for user response.
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case granted := <-respCh:
		if !granted {
			return false, ErrDenied
		}
		return true, nil
	}
}

func (s *permissionService) Grant(id string) {
	s.pendingMu.Lock()
	entry, ok := s.pending[id]
	s.pendingMu.Unlock()
	if ok {
		entry.respCh <- true
	}
}

func (s *permissionService) GrantPersistent(id string) {
	s.pendingMu.Lock()
	entry, ok := s.pending[id]
	s.pendingMu.Unlock()
	if !ok {
		return
	}
	// Cache so future identical requests in the same session are auto-approved.
	s.sessionPermsMu.Lock()
	s.sessionPerms = append(s.sessionPerms, entry.request)
	s.sessionPermsMu.Unlock()

	entry.respCh <- true
}

func (s *permissionService) AutoApproveSession(sessionID string) {
	s.autoSessionsMu.Lock()
	s.autoSessions[sessionID] = true
	s.autoSessionsMu.Unlock()
}

func (s *permissionService) Deny(id string) {
	s.pendingMu.Lock()
	entry, ok := s.pending[id]
	s.pendingMu.Unlock()
	if ok {
		entry.respCh <- false
	}
}

// resolveDir returns the directory component for path.
// If path is empty, ".", or doesn't exist yet, it returns the parent directory.
func (s *permissionService) resolveDir(path string) string {
	if path == "" || path == "." {
		return s.workingDir
	}
	info, err := os.Stat(path)
	if err != nil {
		// Path doesn't exist yet (e.g. a new file about to be written).
		clean := filepath.Clean(path)
		if !filepath.IsAbs(clean) {
			clean = filepath.Join(s.workingDir, clean)
		}
		return filepath.Dir(clean)
	}
	if info.IsDir() {
		return path
	}
	return filepath.Dir(path)
}

// matchesDenyPattern returns the first pattern in patterns that matches cmd,
// or "" if none match.
//
// Matching rules:
//   - Pattern "foo *" (trailing space-star) matches cmd "foo" or any cmd that
//     starts with "foo " — i.e. the command name followed by any arguments.
//   - Pattern "foo*" (bare star, no space) matches any cmd that starts with "foo".
//   - Any other pattern matches cmd exactly, or cmd that starts with pattern+" ".
//     This makes "git push" match "git push", "git push origin main", etc.
func matchesDenyPattern(cmd string, patterns []string) string {
	cmd = strings.TrimSpace(cmd)
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// "foo *" → prefix "foo "
		if strings.HasSuffix(p, " *") {
			prefix := strings.TrimSuffix(p, "*") // keep the trailing space
			if strings.HasPrefix(cmd, prefix) || cmd == strings.TrimSpace(prefix) {
				return p
			}
			continue
		}
		// "foo*" → prefix "foo"
		if strings.HasSuffix(p, "*") {
			prefix := strings.TrimSuffix(p, "*")
			if strings.HasPrefix(cmd, prefix) {
				return p
			}
			continue
		}
		// exact or "git push" matching "git push origin main"
		if cmd == p || strings.HasPrefix(cmd, p+" ") {
			return p
		}
	}
	return ""
}

// newID generates a random request ID (UUID-ish, using crypto/rand).
func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // rand.Read never returns an error per stdlib docs
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
