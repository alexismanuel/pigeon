# pigeon Lua extensions

Extensions are Lua 5.1 scripts that hook into pigeon's lifecycle, register
custom slash commands, and update the status bar. Each extension file runs in
its own isolated `gopher-lua` state.

---

## File locations

| Priority | Directory                              | Scope        |
|----------|----------------------------------------|--------------|
| Low      | `~/.config/pigeon/extensions/*.lua`    | User global  |
| High     | `.pigeon/extensions/*.lua`             | Project-local|

Project-local files override user-global files with the same stem name. All
`.lua` files in both directories are loaded automatically on startup.

---

## Global API — `pigeon.*`

### `pigeon.on(event, fn)`

Register a handler for a lifecycle event.

```lua
pigeon.on("turn_end", function(ev)
  -- ev is a Lua table built from the event data map
end)
```

| Event kind          | When it fires                                        | `ev` fields                    |
|---------------------|------------------------------------------------------|--------------------------------|
| `session_start`     | Once, after the session is initialised               | (empty)                        |
| `input`             | When the user submits a prompt                       | `text` — the raw input         |
| `before_agent_start`| Just before the first model API call                 | (empty)                        |
| `tool_call`         | Before each tool is executed                         | `name`, `args`                 |
| `tool_result`       | After each tool returns                              | `name`, `result`               |
| `turn_end`          | After the agent finishes all tool rounds             | (empty)                        |
| `session_shutdown`  | When the user quits                                  | (empty)                        |

**Return value semantics**

| Handler       | Return value                  | Effect                              |
|---------------|-------------------------------|-------------------------------------|
| `tool_call`   | `false`                       | Block the tool call                 |
| `input`       | a string                      | Replace the user's input            |
| `tool_result` | a string                      | Replace the tool result             |
| any           | `nil` / nothing               | No effect                           |

Multiple handlers for the same event all run; the last non-nil return wins.

---

### `pigeon.register_command(name, desc, fn)`

Register a custom slash command. `name` must start with `/`.

```lua
pigeon.register_command("/status", "show my extension status", function(args)
  -- args is a Lua table; args[1] is the raw argument string after the command name
  local arg = args[1] or ""
  pigeon.set_status("my-ext", "arg was: " .. arg)
end)
```

Registered commands appear in the `/` autocomplete list inside pigeon.

---

### `pigeon.set_status(id, text)`

Update or clear an entry in the status bar at the bottom of the TUI.

```lua
pigeon.set_status("my-ext", "all good")   -- set
pigeon.set_status("my-ext", nil)          -- clear
```

- `id` is an arbitrary unique string per extension; it prevents collisions
  between multiple extensions.
- Multiple statuses are sorted by `id` and joined with `·`.
- Passing `nil` as `text` removes the entry.

---

### `pigeon.env(name) → string | nil`

Read an environment variable.

```lua
local key = pigeon.env("OPENROUTER_API_KEY")
if not key then return end
```

---

### `pigeon.http_get(url [, headers]) → body, err`

Synchronous HTTP GET. Default timeout is 15 s.

```lua
local body, err = pigeon.http_get(
  "https://api.example.com/data",
  { Authorization = "Bearer " .. token }
)
if err then
  pigeon.set_status("my-ext", "fetch error: " .. err)
  return
end
```

Returns `(body_string, nil)` on success or `(nil, error_string)` on failure.

---

### `pigeon.json_decode(str) → table, err`

Parse a JSON string into a Lua table.

```lua
local data, err = pigeon.json_decode(body)
if err then return end
local name = data.name
```

---

### `pigeon.json_encode(value) → str, err`

Encode a Lua value (table, string, number, bool) to a JSON string.

```lua
local payload, err = pigeon.json_encode({ key = "val", count = 42 })
```

Arrays (tables with contiguous integer keys starting at 1) are encoded as JSON
arrays. Mixed or string-keyed tables become JSON objects.

---

## Extension lifecycle

```
load (startup)
  └─ top-level code runs → registers handlers and commands

session_start → before_agent_start
  └─ (user types prompt)
     └─ input → before_agent_start → [tool_call → tool_result]* → turn_end
       └─ (user types prompt)
          └─ ...
             └─ session_shutdown
```

Top-level code must be **fast and non-blocking**. Do not make HTTP calls at
the top level — fire them from event handlers or commands instead.

---

## Full example — word count status

```lua
-- ~/.config/pigeon/extensions/word-count.lua
-- Shows a running word count of the last user prompt in the status bar.

pigeon.on("input", function(ev)
  local words = 0
  for _ in ev.text:gmatch("%S+") do
    words = words + 1
  end
  pigeon.set_status("wc", string.format("%d words", words))
end)

pigeon.on("turn_end", function(_)
  pigeon.set_status("wc", nil)
end)
```

---

## Full example — tool call logger

```lua
-- .pigeon/extensions/tool-log.lua
-- Appends every tool call to a local log file.

local LOG = ".pigeon/tools.log"

pigeon.on("tool_call", function(ev)
  local f = io.open(LOG, "a")
  if f then
    f:write(os.date("[%Y-%m-%d %H:%M:%S] ") .. ev.name .. "  " .. (ev.args or "") .. "\n")
    f:close()
  end
end)
```

---

## Full example — blocking dangerous tools

```lua
-- .pigeon/extensions/guard.lua
-- Blocks rm -rf from bash tool calls.

pigeon.on("tool_call", function(ev)
  if ev.name == "bash" and ev.args:find("rm %-rf") then
    pigeon.set_status("guard", "⛔ blocked rm -rf")
    return false  -- block
  end
end)
```
