package session

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"pigeon/internal/provider/openrouter"
)

type Manager struct {
	baseDir string
}

type SessionMeta struct {
	ID        string
	Path      string
	UpdatedAt time.Time
}

type Node struct {
	ID        string
	ParentID  string
	RecordedAt time.Time
	Message   openrouter.Message
}

type jsonlEntry struct {
	ID         string             `json:"id,omitempty"`
	ParentID   string             `json:"parentId,omitempty"`
	RecordedAt time.Time          `json:"recordedAt"`
	Message    openrouter.Message `json:"message"`
}

type sessionMetaState struct {
	Model string `json:"model,omitempty"`
	Label string `json:"label,omitempty"`
}

func NewManager(baseDir string) *Manager {
	if strings.TrimSpace(baseDir) == "" {
		baseDir = defaultBaseDir()
	}
	return &Manager{baseDir: baseDir}
}

func (m *Manager) Ensure() error {
	return os.MkdirAll(m.baseDir, 0o755)
}

func (m *Manager) NewSession() (string, error) {
	if err := m.Ensure(); err != nil {
		return "", err
	}
	sessionID, err := generateSessionID()
	if err != nil {
		return "", err
	}
	path, err := m.sessionPath(sessionID)
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return sessionID, nil
}

func (m *Manager) SetSessionModel(sessionID, model string) error {
	return m.updateMeta(sessionID, func(s *sessionMetaState) {
		s.Model = strings.TrimSpace(model)
	})
}

func (m *Manager) SetSessionLabel(sessionID, label string) error {
	return m.updateMeta(sessionID, func(s *sessionMetaState) {
		s.Label = strings.TrimSpace(label)
	})
}

func (m *Manager) GetSessionLabel(sessionID string) (string, error) {
	state, err := m.readMeta(sessionID)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(state.Label), nil
}

// updateMeta reads the sidecar, applies fn, and writes it back atomically.
func (m *Manager) updateMeta(sessionID string, fn func(*sessionMetaState)) error {
	if err := m.Ensure(); err != nil {
		return err
	}
	state, err := m.readMeta(sessionID)
	if err != nil {
		return err
	}
	fn(&state)
	path, err := m.metaPath(sessionID)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// readMeta reads the sidecar for sessionID; returns zero value if not found.
func (m *Manager) readMeta(sessionID string) (sessionMetaState, error) {
	path, err := m.metaPath(sessionID)
	if err != nil {
		return sessionMetaState{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return sessionMetaState{}, nil
		}
		return sessionMetaState{}, err
	}
	var state sessionMetaState
	if err := json.Unmarshal(data, &state); err != nil {
		return sessionMetaState{}, fmt.Errorf("decode session meta: %w", err)
	}
	return state, nil
}

func (m *Manager) GetSessionModel(sessionID string) (string, error) {
	state, err := m.readMeta(sessionID)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(state.Model), nil
}

func (m *Manager) AppendMessages(sessionID, parentNodeID string, messages []openrouter.Message) (string, error) {
	if len(messages) == 0 {
		return strings.TrimSpace(parentNodeID), nil
	}
	if err := m.Ensure(); err != nil {
		return "", err
	}
	path, err := m.sessionPath(sessionID)
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	parent := strings.TrimSpace(parentNodeID)
	for _, msg := range messages {
		nodeID, err := generateNodeID()
		if err != nil {
			return "", err
		}
		entry := jsonlEntry{
			ID:         nodeID,
			ParentID:   parent,
			RecordedAt: time.Now().UTC(),
			Message:    msg,
		}
		if err := enc.Encode(entry); err != nil {
			return "", err
		}
		parent = nodeID
	}
	return parent, nil
}

func (m *Manager) LoadLatestMessages(sessionID string) ([]openrouter.Message, string, error) {
	nodes, err := m.ListNodes(sessionID)
	if err != nil {
		return nil, "", err
	}
	if len(nodes) == 0 {
		return nil, "", nil
	}
	latest := nodes[len(nodes)-1]
	messages, err := buildPathMessages(nodes, latest.ID)
	if err != nil {
		return nil, "", err
	}
	return messages, latest.ID, nil
}

func (m *Manager) LoadMessagesAtNode(sessionID, nodeID string) ([]openrouter.Message, error) {
	nodes, err := m.ListNodes(sessionID)
	if err != nil {
		return nil, err
	}
	return buildPathMessages(nodes, nodeID)
}

func (m *Manager) ResolveNodeID(sessionID, prefix string) (string, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return "", fmt.Errorf("node id is required")
	}
	nodes, err := m.ListNodes(sessionID)
	if err != nil {
		return "", err
	}
	if len(nodes) == 0 {
		return "", fmt.Errorf("session has no nodes")
	}
	var matches []string
	for _, node := range nodes {
		if strings.HasPrefix(node.ID, prefix) {
			matches = append(matches, node.ID)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no node matches prefix %q", prefix)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("node prefix %q is ambiguous (%d matches)", prefix, len(matches))
	}
}

func (m *Manager) ListNodes(sessionID string) ([]Node, error) {
	path, err := m.sessionPath(sessionID)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)

	var out []Node
	var previousID string
	legacyCount := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry jsonlEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return nil, fmt.Errorf("decode session line: %w", err)
		}

		nodeID := strings.TrimSpace(entry.ID)
		if nodeID == "" {
			legacyCount++
			nodeID = fmt.Sprintf("legacy-%06d", legacyCount)
		}
		parentID := strings.TrimSpace(entry.ParentID)
		if parentID == "" && previousID != "" {
			parentID = previousID
		}

		out = append(out, Node{
			ID:         nodeID,
			ParentID:   parentID,
			RecordedAt: entry.RecordedAt,
			Message:    entry.Message,
		})
		previousID = nodeID
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (m *Manager) ListSessions(limit int) ([]SessionMeta, error) {
	if err := m.Ensure(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(m.baseDir)
	if err != nil {
		return nil, err
	}

	out := make([]SessionMeta, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(m.baseDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".jsonl")
		out = append(out, SessionMeta{ID: id, Path: path, UpdatedAt: info.ModTime()})
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func buildPathMessages(nodes []Node, nodeID string) ([]openrouter.Message, error) {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return nil, nil
	}
	byID := make(map[string]Node, len(nodes))
	for _, node := range nodes {
		byID[node.ID] = node
	}
	if _, ok := byID[nodeID]; !ok {
		return nil, fmt.Errorf("node not found: %s", nodeID)
	}

	path := make([]Node, 0, len(nodes))
	cursor := nodeID
	for step := 0; step <= len(nodes); step++ {
		node, ok := byID[cursor]
		if !ok {
			break
		}
		path = append(path, node)
		if strings.TrimSpace(node.ParentID) == "" {
			break
		}
		cursor = node.ParentID
	}
	if len(path) > len(nodes)+1 {
		return nil, fmt.Errorf("invalid parent chain")
	}

	messages := make([]openrouter.Message, 0, len(path))
	for i := len(path) - 1; i >= 0; i-- {
		messages = append(messages, path[i].Message)
	}
	return messages, nil
}

func defaultBaseDir() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return filepath.Join(".pigeon", "sessions")
	}
	return filepath.Join(home, ".pigeon", "sessions")
}

func generateSessionID() (string, error) {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s", time.Now().UTC().Format("20060102-150405"), hex.EncodeToString(buf)), nil
}

func generateNodeID() (string, error) {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return fmt.Sprintf("n-%s", hex.EncodeToString(buf)), nil
}

// GetFirstUserMessage returns the content of the first user message in the
// session, or "" if none exists. Reads the JSONL line-by-line and stops early.
func (m *Manager) GetFirstUserMessage(sessionID string) (string, error) {
	path, err := m.sessionPath(sessionID)
	if err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var entry jsonlEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry.Message.Role == "user" && strings.TrimSpace(entry.Message.Content) != "" {
			return strings.TrimSpace(entry.Message.Content), nil
		}
	}
	return "", scanner.Err()
}

func (m *Manager) metaPath(sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", fmt.Errorf("session id is required")
	}
	if strings.Contains(sessionID, "/") || strings.Contains(sessionID, `\\`) {
		return "", fmt.Errorf("invalid session id: %s", sessionID)
	}
	return filepath.Join(m.baseDir, sessionID+".meta.json"), nil
}

func (m *Manager) sessionPath(sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", fmt.Errorf("session id is required")
	}
	if strings.Contains(sessionID, "/") || strings.Contains(sessionID, `\\`) {
		return "", fmt.Errorf("invalid session id: %s", sessionID)
	}
	return filepath.Join(m.baseDir, sessionID+".jsonl"), nil
}
