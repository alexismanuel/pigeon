# Self-extension guide

pigeon is designed to be extended from within a pigeon session. This guide
explains every extension mechanism in order of increasing complexity.

---

## 1. Prompt templates — instant shortcuts

**What**: Save a frequently typed prompt so you can invoke it with `/name`.

**When to create**: You notice yourself typing the same thing more than twice.

**How**:
```bash
mkdir -p ~/.config/pigeon/prompts
# Write the prompt content
cat > ~/.config/pigeon/prompts/my-prompt.md << 'EOF'
<paste prompt content here>
EOF
```

Or from inside pigeon:
```
Write the content of my new prompt template to ~/.config/pigeon/prompts/review.md
```

Templates take effect after restarting pigeon (or in a new session).
See [prompts.md](prompts.md) for format details.

---

## 2. Skills — reusable instruction sets

**What**: A SKILL.md injected as a system message on demand via `/skill:name`.

**When to create**: You have a recurring task with specific conventions (e.g.
"write Go tests my way", "follow our commit format", "always use our API
client pattern").

**How**:
```bash
mkdir -p ~/.config/pigeon/skills/my-skill
cat > ~/.config/pigeon/skills/my-skill/SKILL.md << 'EOF'
# My skill

Use when: ...

## Rules
1. ...
EOF
```

Or ask pigeon to write it for you:
```
Create a skill called "go-testing" in ~/.config/pigeon/skills/go-testing/SKILL.md
that enforces our testing conventions: table-driven, no testify, use t.TempDir.
```

See [skills.md](skills.md) for the recommended SKILL.md structure.

---

## 3. System prompt — change the core persona

**What**: A Markdown file that acts as the session-wide system prompt.

**Priority chain** (highest first):
1. `-system` CLI flag
2. `.pigeon/system.md` — project-local instructions
3. `~/.config/pigeon/system.md` — user-global default

**When to use**:
- Add project-specific context to `.pigeon/system.md` (architecture overview,
  coding standards, key file paths).
- Change the global default in `~/.config/pigeon/system.md`.
- Override temporarily with `/system <text>` inside a session.

**Example project-local system.md**:
```markdown
This is the pigeon repository. Module path: `pigeon`. Go 1.22.

Key files:
- internal/tui/model.go — TUI state machine
- internal/agent/agent.go — tool-call loop
- internal/extensions/lua/runtime.go — Lua runtime

When making changes: read the file first, run go build ./... and go test -race ./...
after every edit.
```

---

## 4. Lua extensions — behaviour hooks and commands

**What**: Lua scripts that hook into lifecycle events, register commands, and
update the status bar.

**When to create**:
- You want to react to tool calls, model turns, or user input.
- You want a new `/command` without recompiling Go.
- You want a live indicator in the status bar.

**Quickstart**:

```lua
-- ~/.config/pigeon/extensions/hello.lua
pigeon.on("session_start", function(_ev)
  pigeon.set_status("hello", "👋 ready")
end)

pigeon.register_command("/hello", "greet the user", function(_args)
  pigeon.set_status("hello", "Hello from Lua!")
end)
```

Drop the file in place and restart pigeon. The `/hello` command will appear
in autocomplete.

**Extensions load order**:
1. `~/.config/pigeon/extensions/*.lua` (alphabetical)
2. `.pigeon/extensions/*.lua` (alphabetical, same name overrides global)

See [extensions.md](extensions.md) for the complete API reference.

**Patterns**

| Goal | Pattern |
|---|---|
| Status bar indicator | `pigeon.on("turn_end", ...)` + `pigeon.set_status(...)` |
| Tool audit log | `pigeon.on("tool_call", ...)` + `io.open(log, "a")` |
| Block dangerous commands | `pigeon.on("tool_call", ...)` + `return false` |
| Fetch external data | `pigeon.http_get(url, headers)` inside a command |
| New slash command | `pigeon.register_command("/name", desc, fn)` |
| Modify user input | `pigeon.on("input", ...)` + `return "modified text"` |

---

## 5. Go code — deep changes

For changes that cannot be expressed in Lua (new tools, new TUI modes, new
commands built into the binary), edit the Go source directly.

**Setup check**:
```bash
cd /path/to/pigeon
go build ./...   # should produce no output
go test -race ./... -timeout=60s  # all packages green
```

**Key entry points by goal**:

| Goal | File | What to do |
|---|---|---|
| New built-in tool | `internal/tools/tools.go` | Add ToolDefinition + execFoo method + Execute case |
| New `/command` | `internal/tui/commands.go` + `model.go` | Add to builtinCommands + handleCommand case |
| New TUI mode | `internal/tui/model.go` | Add appMode const, picker struct, updateFoo, View branch |
| New session field | `internal/session/session.go` | Add to sessionMetaState, add Set/Get methods using updateMeta |
| New Lua API function | `internal/extensions/lua/runtime.go` | Add glua.LGFunction, register in registerAPI |
| New event kind | `internal/extensions/lua/runtime.go` | Add EventKind const, Fire from the right place |
| New config flag | `cmd/pigeon/main.go` | Add flag.String/Bool, wire to NewModel or agent |

**Workflow**:
1. Read the relevant files first.
2. Make changes.
3. `go build ./...` — fix any compile errors.
4. `go test -race ./... -timeout=60s` — all tests must pass.
5. For TUI changes: manually test with `go run ./cmd/pigeon`.

**Test conventions** (see `internal/tui/*_test.go` for examples):
- Use `newTestModel()` for a zero-value TUI model without real dependencies.
- Use `newModelWithSessions(t)` for a TUI model with a real `session.Manager`
  in a temp dir.
- Fake implementations of interfaces are inline structs in the test file
  (no mocking libraries).
- Run with `-race` always; pigeon uses goroutines for streaming.

---

## Choosing the right extension level

```
Need changes immediately, no restart?
  └─ /system <text>        change system prompt inline
  └─ /skill:<name>         inject a skill system message

Need something persistent but no Go recompile?
  └─ Lua extension         for behaviour hooks, commands, status
  └─ Skill file            for reusable instruction sets
  └─ Prompt template       for input shortcuts
  └─ system.md             for project/global context

Need a new tool, new TUI feature, or deep integration?
  └─ Edit Go source        follow the architecture guide above
```

---

## Documenting your extension

When you create an extension, skill, or new feature, document it:

- **Lua extension**: add a comment block at the top of the `.lua` file
  explaining purpose, events handled, commands registered, and status bar IDs.
- **Skill**: the SKILL.md is the documentation.
- **Go changes**: update `docs/architecture.md` if you add a new package or
  significant subsystem. Add inline godoc comments to exported types and
  functions.
- **New commands**: update `builtinCommands` with a clear `desc` field — it
  appears in the autocomplete overlay.
