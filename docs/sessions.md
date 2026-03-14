# pigeon sessions

Sessions are the persistence layer for pigeon conversations. Each session is a
JSONL file stored in `~/.pigeon/sessions/`. The format supports **branching** —
multiple conversation paths can diverge from a shared prefix.

---

## Storage layout

```
~/.pigeon/sessions/
  <session-id>.jsonl       ← conversation messages as JSONL nodes
  <session-id>.meta.json   ← sidecar: model, label
```

`<session-id>` is a random 16-character hex string, e.g.
`3f9a2c1b4d8e7a05`.

---

## JSONL format

Each line in `.jsonl` is a JSON object:

```json
{
  "id": "a1b2c3d4e5f60001",
  "parentId": "a1b2c3d4e5f60000",
  "recordedAt": "2026-03-13T18:30:00Z",
  "message": { "role": "user", "content": "explain goroutines" }
}
```

| Field        | Description                                                |
|--------------|------------------------------------------------------------|
| `id`         | 16-char hex node identifier (unique in the file)           |
| `parentId`   | Parent node ID, or `""` for the root                       |
| `recordedAt` | UTC timestamp                                              |
| `message`    | An OpenRouter-compatible message (`role` + `content`)      |

Messages are appended in order. Each `AppendMessages` call writes one node per
message, chaining `parentId` links.

---

## Branching

Branching happens naturally when you resume a session at an older node and
continue from there. The new messages get the old node as `parentId`, creating
a fork:

```
root
  └─ A (user: "write a parser")
       └─ B (assistant: "here's a recursive parser")
            ├─ C (user: "make it iterative")   ← branch 1
            └─ D (user: "add error recovery")  ← branch 2 (resumed from B)
```

`/tree` visualises this:
- **Linear** (no branching): vertical bullet list
- **Branched**: ASCII tree with `├─` and `└─` connectors

---

## Session meta sidecar

`<session-id>.meta.json` stores session-level state:

```json
{
  "model": "openai/gpt-4o",
  "label": "parser refactor"
}
```

Both fields are optional. `model` is set by `/model` and persisted across
restarts. `label` is set by `/label` to give a session a human-readable name.

---

## Commands

| Command             | Action                                                    |
|---------------------|-----------------------------------------------------------|
| `/sessions`         | Open interactive session picker (search, arrow keys, enter)|
| `/new`              | Start a fresh session                                     |
| `/label [text]`     | Set or show a label for the current session               |
| `/tree`             | Show the current session's conversation tree              |

---

## Session picker

`/sessions` opens a full-screen interactive picker:

- **Columns**: short session ID · label/first-prompt preview · relative age
- **Search**: type to filter by ID, label, or first user message
- **Navigation**: ↑/↓ or Ctrl+N/Ctrl+P to move, Enter to load, Esc to cancel

If a session has a label it shows as `[label] first prompt…`. Unlabelled
sessions show the first user message directly.

---

## Programmatic access

The session package (`internal/session`) exposes:

```go
// Load conversation up to latest node
messages, nodeID, err := manager.LoadLatestMessages(sessionID)

// Load conversation up to a specific node (for branching)
messages, err := manager.LoadMessagesAtNode(sessionID, nodeID)

// Resolve a node by prefix (first 4+ chars of node ID)
fullNodeID, err := manager.ResolveNodeID(sessionID, prefix)

// Append new messages, returns the new leaf node ID
nodeID, err := manager.AppendMessages(sessionID, parentNodeID, messages)

// List all nodes (full tree structure)
nodes, err := manager.ListNodes(sessionID)

// Label the session
err := manager.SetSessionLabel(sessionID, "my label")

// Get first user message (for display in pickers)
text, err := manager.GetFirstUserMessage(sessionID)
```
