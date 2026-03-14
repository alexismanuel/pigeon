# pigeon architecture

pigeon is a local-first Go coding agent TUI backed by OpenRouter.

```
cmd/pigeon/main.go           ← binary entry point, wires all packages
internal/
  agent/         ← agentic tool-call loop
  app/           ← startup helpers (API key resolution)
  config/        ← config directory paths, system prompt resolution
  extensions/lua/← Lua extension runtime (gopher-lua)
  provider/openrouter/ ← SSE streaming chat client + model list
  resources/     ← skill / prompt / extension discovery
  session/       ← JSONL session persistence (branching)
  tools/         ← built-in tool executors (read/write/edit/bash)
  tui/           ← Bubble Tea TUI (chat, pickers, viewport)
docs/            ← reference documentation (this file and siblings)
e2e/             ← PTY-driven end-to-end tests
```

Go module path: `pigeon` (bare, not github.com/…).
Go version: 1.22 (`max` builtin available; `range N` loops work).

---

## Package responsibilities

### `internal/agent`

`agent.New(client)` wraps the OpenRouter client in a tool-call loop.

```go
type TurnCallbacks struct {
    OnToken        func(token string)
    OnToolEvent    func(kind, name, content string)
    BeforeToolCall func(name, args string) bool  // return true to block
}

newMessages, err := ag.RunTurn(ctx, model, history, userInput, callbacks)
```

- Streams tokens via SSE; calls `OnToken` for each chunk.
- On each tool-call delta, accumulates arguments then calls `BeforeToolCall`.
  If blocked, substitutes `[tool call 'X' blocked by extension]` as the result.
- `OnToolEvent` fires for `tool_call` and `tool_result` events (used by TUI to
  render live tool activity).
- Max tool rounds: 12 (prevents runaway loops).
- Returns all new messages (user + assistant + tool results) for persistence.

### `internal/provider/openrouter`

SSE streaming client for the OpenRouter API.

```go
client := openrouter.NewClient(apiKey, nil)
client.SetAttribution("pigeon", "")

// Streaming turn
lastMsg, err := client.StreamChatCompletion(ctx, model, messages, tools, handler)

// Model catalogue
models, err := client.ListModels(ctx)
```

`StreamHandler` receives `StreamEvent{Type, Delta, ToolCall{Name,ID,Args}}` per chunk.
Tool call argument deltas are accumulated internally; `finalizeToolCalls`
assembles them into complete `ToolCall` structs before returning.

### `internal/tools`

`Executor` implements `read`, `write`, `edit`, `bash`.

```go
exec := tools.NewExecutor()
defs := exec.Definitions()            // []openrouter.ToolDefinition
result, err := exec.Execute(ctx, name, argsJSON)
```

All paths are resolved relative to the cwd at the time `NewExecutor()` is called.
Output is truncated at 2000 lines or 50 KB.

### `internal/session`

See [sessions.md](sessions.md) for the full data model.

Key interface the TUI depends on (defined as `sessionStore` in `internal/tui`):

```go
NewSession() (string, error)
AppendMessages(sessionID, parentNodeID string, messages []Message) (string, error)
LoadLatestMessages(sessionID string) ([]Message, string, error)
LoadMessagesAtNode(sessionID, nodeID string) ([]Message, error)
ResolveNodeID(sessionID, prefix string) (string, error)
ListNodes(sessionID string) ([]Node, error)
ListSessions(limit int) ([]SessionMeta, error)
SetSessionModel(sessionID, model string) error
GetSessionModel(sessionID string) (string, error)
SetSessionLabel(sessionID, label string) error
GetSessionLabel(sessionID string) (string, error)
GetFirstUserMessage(sessionID string) (string, error)
```

### `internal/resources`

Discovers skills, prompt templates, and Lua extension paths from
`~/.config/pigeon/` and `.pigeon/` (project wins on conflict).

See [skills.md](skills.md), [prompts.md](prompts.md), [extensions.md](extensions.md).

### `internal/extensions/lua`

Each `.lua` file gets its own `*gopher-lua.LState` with a `sync.Mutex`.
The `Runtime` manages all states and dispatches events.

See [extensions.md](extensions.md) for the full Lua API.

### `internal/config`

`ResolveSystemPrompt(cliFlag)` — priority chain for system prompt:
1. `-system` CLI flag
2. `.pigeon/system.md` (project-local)
3. `~/.config/pigeon/system.md` (user global)
4. Compiled-in default (this prompt)

### `internal/tui`

Bubble Tea model (`Model`) drives the full TUI. Key subcomponents:

- `picker` — interactive model picker (live search + cursor)
- `sessionPicker` — interactive session picker (same UX)
- `commands.go` — `/`-command autocomplete (builtins + resource/extension cmds)
- `update_test.go`, `model_test.go`, `picker_test.go`, etc. — unit tests

Modes: `chatMode`, `pickerMode`, `resumeMode`.

`submitPrompt` wires the agent loop to the TUI via a `chan tea.Msg` goroutine.

---

## Key data flow — one turn

```
User types Enter
  → handleCommand (if starts with /)
  → submitPrompt
      creates streamCh chan tea.Msg
      launches goroutine:
        Lua: Fire(EventBeforeAgent)
        agent.RunTurn(ctx, model, history, input, callbacks)
          callbacks.OnToken       → tokenMsg  → streamCh
          callbacks.OnToolEvent   → toolCallMsg/toolResultMsg → streamCh
          callbacks.BeforeToolCall → Lua: Fire(EventToolCall) → block?
        → turnDoneMsg{newMessages} → streamCh
      TUI reads streamCh via waitForStreamMsg
        tokenMsg      → append token to current assistant line
        toolCallMsg   → render "⚙ tool(…)" line, reset assistant idx
        toolResultMsg → render result preview line
        turnDoneMsg   → append newMessages to history, persist to session
```

---

## TUI colour palette

| Style         | Color (256-color) | Used for             |
|---------------|-------------------|----------------------|
| `headerStyle` | blue 12           | Header bar           |
| `errorStyle`  | red 9             | Error messages       |
| `userStyle`   | green 10          | "You:" lines         |
| `asstStyle`   | cyan 14           | "Assistant:" lines   |
| `metaStyle`   | grey 8            | Session info, hints  |

---

## Adding a built-in command

1. Add a `{"/cmd", "[args]", "desc"}` entry to `builtinCommands` in
   `internal/tui/commands.go`.
2. Add a `case "/cmd":` branch in `handleCommand` in
   `internal/tui/model.go`.
3. Add tests in `internal/tui/update_test.go`.
4. Run `go test -race ./...` to confirm nothing is broken.

## Adding a new tool

1. Add a `ToolDefinition` to `Executor.Definitions()` in
   `internal/tools/tools.go`.
2. Add an `execFoo(argumentsJSON string)` method.
3. Add a `case "foo":` to `Executor.Execute`.
4. Add tests in `internal/tools/tools_test.go`.
5. Run `go build ./...` and `go test -race ./...`.
