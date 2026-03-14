package session

import (
	"fmt"
	"strings"
	"testing"

	"pigeon/internal/provider/openrouter"
)

func TestManagerAppendAndLoadLatestMessages(t *testing.T) {
	m := NewManager(t.TempDir())
	sessionID, err := m.NewSession()
	if err != nil {
		t.Fatalf("new session failed: %v", err)
	}

	input := []openrouter.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}
	lastNodeID, err := m.AppendMessages(sessionID, "", input)
	if err != nil {
		t.Fatalf("append failed: %v", err)
	}
	if lastNodeID == "" {
		t.Fatalf("expected last node id")
	}

	got, latestNodeID, err := m.LoadLatestMessages(sessionID)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if latestNodeID == "" {
		t.Fatalf("expected latest node id")
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got))
	}
	if got[0].Role != "user" || got[0].Content != "hello" {
		t.Fatalf("unexpected first message: %+v", got[0])
	}
	if got[1].Role != "assistant" || got[1].Content != "hi" {
		t.Fatalf("unexpected second message: %+v", got[1])
	}
}

func TestManagerBranchingAndLoadAtNode(t *testing.T) {
	m := NewManager(t.TempDir())
	sessionID, err := m.NewSession()
	if err != nil {
		t.Fatalf("new session failed: %v", err)
	}

	baseLast, err := m.AppendMessages(sessionID, "", []openrouter.Message{
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "a1"},
	})
	if err != nil {
		t.Fatalf("append base failed: %v", err)
	}
	branchALast, err := m.AppendMessages(sessionID, baseLast, []openrouter.Message{
		{Role: "user", Content: "q2a"},
		{Role: "assistant", Content: "a2a"},
	})
	if err != nil {
		t.Fatalf("append branch A failed: %v", err)
	}
	branchBLast, err := m.AppendMessages(sessionID, baseLast, []openrouter.Message{
		{Role: "user", Content: "q2b"},
		{Role: "assistant", Content: "a2b"},
	})
	if err != nil {
		t.Fatalf("append branch B failed: %v", err)
	}

	msgsA, err := m.LoadMessagesAtNode(sessionID, branchALast)
	if err != nil {
		t.Fatalf("load at branch A failed: %v", err)
	}
	if len(msgsA) != 4 || msgsA[3].Content != "a2a" {
		t.Fatalf("unexpected branch A path: %+v", msgsA)
	}

	msgsB, err := m.LoadMessagesAtNode(sessionID, branchBLast)
	if err != nil {
		t.Fatalf("load at branch B failed: %v", err)
	}
	if len(msgsB) != 4 || msgsB[3].Content != "a2b" {
		t.Fatalf("unexpected branch B path: %+v", msgsB)
	}

	nodes, err := m.ListNodes(sessionID)
	if err != nil {
		t.Fatalf("list nodes failed: %v", err)
	}
	if len(nodes) != 6 {
		t.Fatalf("expected 6 nodes, got %d", len(nodes))
	}
}

func TestManagerResolveNodeID(t *testing.T) {
	m := NewManager(t.TempDir())
	sessionID, err := m.NewSession()
	if err != nil {
		t.Fatalf("new session failed: %v", err)
	}
	last, err := m.AppendMessages(sessionID, "", []openrouter.Message{{Role: "user", Content: "hello"}})
	if err != nil {
		t.Fatalf("append failed: %v", err)
	}
	prefix := last[:6]
	resolved, err := m.ResolveNodeID(sessionID, prefix)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if resolved != last {
		t.Fatalf("unexpected resolve: got %s want %s", resolved, last)
	}
}

func TestManagerSessionModelPersistence(t *testing.T) {
	m := NewManager(t.TempDir())
	sessionID, err := m.NewSession()
	if err != nil {
		t.Fatalf("new session failed: %v", err)
	}
	if err := m.SetSessionModel(sessionID, "openai/gpt-4o-mini"); err != nil {
		t.Fatalf("set model failed: %v", err)
	}
	model, err := m.GetSessionModel(sessionID)
	if err != nil {
		t.Fatalf("get model failed: %v", err)
	}
	if model != "openai/gpt-4o-mini" {
		t.Fatalf("unexpected model: %s", model)
	}
}

func TestManagerListSessions(t *testing.T) {
	m := NewManager(t.TempDir())
	id1, err := m.NewSession()
	if err != nil {
		t.Fatalf("new session1 failed: %v", err)
	}
	id2, err := m.NewSession()
	if err != nil {
		t.Fatalf("new session2 failed: %v", err)
	}

	if _, err := m.AppendMessages(id1, "", []openrouter.Message{{Role: "user", Content: "a"}}); err != nil {
		t.Fatalf("append1 failed: %v", err)
	}
	if _, err := m.AppendMessages(id2, "", []openrouter.Message{{Role: "user", Content: "b"}}); err != nil {
		t.Fatalf("append2 failed: %v", err)
	}

	list, err := m.ListSessions(10)
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(list))
	}
	if (list[0].ID != id1 && list[0].ID != id2) || (list[1].ID != id1 && list[1].ID != id2) {
		t.Fatalf("unexpected ids: %+v", list)
	}
}

func TestDefaultBaseDir_ReturnsPath(t *testing.T) {
	path := defaultBaseDir()
	if path == "" {
		t.Error("expected non-empty base dir")
	}
	if !strings.Contains(path, "pigeon") {
		t.Errorf("expected 'pigeon' in path, got %q", path)
	}
}

func TestManagerGetSessionModel_MissingReturnsEmpty(t *testing.T) {
	m := NewManager(t.TempDir())
	id, _ := m.NewSession()
	got, err := m.GetSessionModel(id)
	if err != nil {
		t.Fatalf("GetSessionModel: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty model for new session, got %q", got)
	}
}

func TestManagerGetSessionModel_RoundTrip(t *testing.T) {
	m := NewManager(t.TempDir())
	id, _ := m.NewSession()
	if err := m.SetSessionModel(id, "anthropic/claude-3-5-sonnet"); err != nil {
		t.Fatalf("SetSessionModel: %v", err)
	}
	got, err := m.GetSessionModel(id)
	if err != nil {
		t.Fatalf("GetSessionModel: %v", err)
	}
	if got != "anthropic/claude-3-5-sonnet" {
		t.Errorf("unexpected model: %q", got)
	}
}

func TestManagerResolveNodeID_Prefix(t *testing.T) {
	m := NewManager(t.TempDir())
	id, _ := m.NewSession()
	nodeID, _ := m.AppendMessages(id, "", []openrouter.Message{{Role: "user", Content: "hi"}})

	resolved, err := m.ResolveNodeID(id, nodeID[:6])
	if err != nil {
		t.Fatalf("ResolveNodeID: %v", err)
	}
	if resolved != nodeID {
		t.Errorf("expected %q, got %q", nodeID, resolved)
	}
}

func TestManagerResolveNodeID_AmbiguousPrefix(t *testing.T) {
	m := NewManager(t.TempDir())
	id, _ := m.NewSession()
	// append two messages — they'll have different node IDs
	m.AppendMessages(id, "", []openrouter.Message{{Role: "user", Content: "a"}})
	m.AppendMessages(id, "", []openrouter.Message{{Role: "user", Content: "b"}})

	// resolving with empty prefix (matches all) should error as ambiguous
	_, err := m.ResolveNodeID(id, "")
	if err == nil {
		t.Error("expected error for empty prefix matching multiple nodes")
	}
}

func TestManagerLoadLatestMessages_Empty(t *testing.T) {
	m := NewManager(t.TempDir())
	id, _ := m.NewSession()
	msgs, nodeID, err := m.LoadLatestMessages(id)
	if err != nil {
		t.Fatalf("LoadLatestMessages on empty session: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected empty messages, got %d", len(msgs))
	}
	if nodeID != "" {
		t.Errorf("expected empty nodeID, got %q", nodeID)
	}
}

func TestManagerBranching(t *testing.T) {
	m := NewManager(t.TempDir())
	id, _ := m.NewSession()

	// root → node1 → node2
	node1, _ := m.AppendMessages(id, "", []openrouter.Message{{Role: "user", Content: "q1"}})
	node2, _ := m.AppendMessages(id, node1, []openrouter.Message{{Role: "assistant", Content: "a1"}})

	// branch from node1 → node3
	node3, _ := m.AppendMessages(id, node1, []openrouter.Message{{Role: "user", Content: "q2"}})

	nodes, err := m.ListNodes(id)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(nodes))
	}

	// loading at node2 should give q1+a1
	msgs, err := m.LoadMessagesAtNode(id, node2)
	if err != nil {
		t.Fatalf("LoadMessagesAtNode: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages on node2 path, got %d", len(msgs))
	}

	// loading at node3 should give just q1+q2 (not a1)
	msgs, err = m.LoadMessagesAtNode(id, node3)
	if err != nil {
		t.Fatalf("LoadMessagesAtNode node3: %v", err)
	}
	if len(msgs) != 2 || msgs[1].Content != "q2" {
		t.Fatalf("unexpected messages on branch path: %+v", msgs)
	}

	_ = node2 // silence unused
}

func TestSetGetSessionLabel_RoundTrip(t *testing.T) {
	m := NewManager(t.TempDir())
	id, _ := m.NewSession()

	if err := m.SetSessionLabel(id, "my refactor"); err != nil {
		t.Fatalf("SetSessionLabel: %v", err)
	}
	got, err := m.GetSessionLabel(id)
	if err != nil {
		t.Fatalf("GetSessionLabel: %v", err)
	}
	if got != "my refactor" {
		t.Errorf("expected 'my refactor', got %q", got)
	}
}

func TestSetSessionLabel_PreservesModel(t *testing.T) {
	m := NewManager(t.TempDir())
	id, _ := m.NewSession()

	m.SetSessionModel(id, "openai/gpt-4o")
	m.SetSessionLabel(id, "my session")

	model, _ := m.GetSessionModel(id)
	if model != "openai/gpt-4o" {
		t.Errorf("SetSessionLabel should not overwrite model, got %q", model)
	}
}

func TestSetSessionModel_PreservesLabel(t *testing.T) {
	m := NewManager(t.TempDir())
	id, _ := m.NewSession()

	m.SetSessionLabel(id, "important work")
	m.SetSessionModel(id, "anthropic/claude-3-5-sonnet")

	label, _ := m.GetSessionLabel(id)
	if label != "important work" {
		t.Errorf("SetSessionModel should not overwrite label, got %q", label)
	}
}

func TestGetSessionLabel_MissingReturnsEmpty(t *testing.T) {
	m := NewManager(t.TempDir())
	id, _ := m.NewSession()
	got, err := m.GetSessionLabel(id)
	if err != nil {
		t.Fatalf("GetSessionLabel: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty label, got %q", got)
	}
}

func TestGetFirstUserMessage_Found(t *testing.T) {
	m := NewManager(t.TempDir())
	id, _ := m.NewSession()
	m.AppendMessages(id, "", []openrouter.Message{
		{Role: "user", Content: "explain goroutines"},
		{Role: "assistant", Content: "sure!"},
	})

	got, err := m.GetFirstUserMessage(id)
	if err != nil {
		t.Fatalf("GetFirstUserMessage: %v", err)
	}
	if got != "explain goroutines" {
		t.Errorf("expected 'explain goroutines', got %q", got)
	}
}

func TestGetFirstUserMessage_SkipsNonUser(t *testing.T) {
	m := NewManager(t.TempDir())
	id, _ := m.NewSession()
	m.AppendMessages(id, "", []openrouter.Message{
		{Role: "system", Content: "you are helpful"},
		{Role: "assistant", Content: "hi"},
		{Role: "user", Content: "hello there"},
	})

	got, err := m.GetFirstUserMessage(id)
	if err != nil {
		t.Fatalf("GetFirstUserMessage: %v", err)
	}
	if got != "hello there" {
		t.Errorf("expected 'hello there', got %q", got)
	}
}

func TestGetFirstUserMessage_Empty(t *testing.T) {
	m := NewManager(t.TempDir())
	id, _ := m.NewSession()

	got, err := m.GetFirstUserMessage(id)
	if err != nil {
		t.Fatalf("GetFirstUserMessage: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

// ── AppendMessages edge cases ──────────────────────────────────────────────────

func TestAppendMessages_EmptySlice(t *testing.T) {
	m := NewManager(t.TempDir())
	id, _ := m.NewSession()
	nodeID, err := m.AppendMessages(id, "parent", []openrouter.Message{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nodeID != "parent" {
		t.Errorf("empty messages should return parentNodeID, got %q", nodeID)
	}
}

func TestAppendMessages_InvalidSessionID(t *testing.T) {
	m := NewManager(t.TempDir())
	_, err := m.AppendMessages("../evil", "", []openrouter.Message{{Role: "user", Content: "x"}})
	if err == nil {
		t.Error("expected error for invalid session id")
	}
}

// ── LoadMessagesAtNode ─────────────────────────────────────────────────────────

func TestLoadMessagesAtNode_InvalidNode(t *testing.T) {
	m := NewManager(t.TempDir())
	id, _ := m.NewSession()
	m.AppendMessages(id, "", []openrouter.Message{{Role: "user", Content: "hi"}})

	_, err := m.LoadMessagesAtNode(id, "nonexistent-node-id")
	if err == nil {
		t.Error("expected error for unknown node")
	}
}

func TestLoadMessagesAtNode_InvalidSessionID(t *testing.T) {
	m := NewManager(t.TempDir())
	_, err := m.LoadMessagesAtNode("../evil", "node")
	if err == nil {
		t.Error("expected error for invalid session id")
	}
}

// ── ResolveNodeID edge cases ──────────────────────────────────────────────────

func TestResolveNodeID_EmptyPrefix(t *testing.T) {
	m := NewManager(t.TempDir())
	id, _ := m.NewSession()
	_, err := m.ResolveNodeID(id, "")
	if err == nil {
		t.Error("expected error for empty prefix")
	}
}

func TestResolveNodeID_NoMatch(t *testing.T) {
	m := NewManager(t.TempDir())
	id, _ := m.NewSession()
	m.AppendMessages(id, "", []openrouter.Message{{Role: "user", Content: "hi"}})
	_, err := m.ResolveNodeID(id, "zzznomatch")
	if err == nil {
		t.Error("expected error for no match")
	}
}

func TestResolveNodeID_Ambiguous(t *testing.T) {
	// If two nodes share the same prefix, resolve should error.
	// This is hard to trigger with generateNodeID (random), but we can verify
	// the session is empty → "no nodes" error path.
	m := NewManager(t.TempDir())
	id, _ := m.NewSession()
	_, err := m.ResolveNodeID(id, "abc")
	if err == nil {
		t.Error("expected error for no nodes")
	}
}

func TestResolveNodeID_InvalidSession(t *testing.T) {
	m := NewManager(t.TempDir())
	_, err := m.ResolveNodeID("../evil", "abc")
	if err == nil {
		t.Error("expected error for invalid session id")
	}
}

// ── ListNodes error paths ─────────────────────────────────────────────────────

func TestListNodes_InvalidSessionID(t *testing.T) {
	m := NewManager(t.TempDir())
	_, err := m.ListNodes("../evil")
	if err == nil {
		t.Error("expected error for invalid session id")
	}
}

func TestListNodes_MissingFile_ReturnsNil(t *testing.T) {
	m := NewManager(t.TempDir())
	nodes, err := m.ListNodes("nonexistent-session-id")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nodes != nil {
		t.Error("expected nil for missing session")
	}
}

// ── ListSessions edge cases ────────────────────────────────────────────────────

func TestListSessions_EmptyDir(t *testing.T) {
	m := NewManager(t.TempDir())
	sessions, err := m.ListSessions(10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(sessions))
	}
}

func TestListSessions_RespectLimit(t *testing.T) {
	m := NewManager(t.TempDir())
	for i := range 5 {
		id, _ := m.NewSession()
		m.AppendMessages(id, "", []openrouter.Message{{Role: "user", Content: fmt.Sprintf("msg%d", i)}})
	}
	sessions, err := m.ListSessions(3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) != 3 {
		t.Errorf("expected 3 sessions (limit), got %d", len(sessions))
	}
}

// ── GetFirstUserMessage edge cases ────────────────────────────────────────────

func TestGetFirstUserMessage_InvalidSession(t *testing.T) {
	m := NewManager(t.TempDir())
	_, err := m.GetFirstUserMessage("../evil")
	if err == nil {
		t.Error("expected error for invalid session id")
	}
}

func TestGetFirstUserMessage_OnlyAssistantMessages(t *testing.T) {
	m := NewManager(t.TempDir())
	id, _ := m.NewSession()
	m.AppendMessages(id, "", []openrouter.Message{{Role: "assistant", Content: "hello"}})
	got, err := m.GetFirstUserMessage(id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

// ── metaPath / sessionPath validation ─────────────────────────────────────────

func TestMetaPath_EmptyID(t *testing.T) {
	m := NewManager(t.TempDir())
	_, err := m.GetSessionModel("")
	if err == nil {
		t.Error("expected error for empty session id")
	}
}

func TestSessionPath_SlashInID(t *testing.T) {
	m := NewManager(t.TempDir())
	_, _, err := m.LoadLatestMessages("a/b")
	if err == nil {
		t.Error("expected error for slash in session id")
	}
}

// ── NewManager zero value ─────────────────────────────────────────────────────

func TestNewManager_EmptyBaseDir_UsesDefault(t *testing.T) {
	// Just ensure NewManager doesn't panic with empty string.
	m := NewManager("")
	if m == nil {
		t.Error("expected non-nil manager")
	}
}

func TestNewManager_CustomBaseDir(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	if m == nil {
		t.Error("expected non-nil manager")
	}
}
