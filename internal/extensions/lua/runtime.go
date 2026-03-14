// Package lua provides the Lua extension runtime for pigeon.
//
// Each extension file gets its own isolated *lua.LState. The runtime
// exposes a "pigeon" global table with the following API:
//
//	pigeon.on(event, fn)                    register an event handler
//	pigeon.register_command(name, desc, fn) register a custom slash command
//	pigeon.set_status(id, text|nil)         update/clear the status bar
//	pigeon.env(name)          → string|nil  read an env variable
//	pigeon.http_get(url, hdrs) → body, err  synchronous HTTP GET
//	pigeon.json_decode(str)   → table, err  parse JSON
//	pigeon.json_encode(table) → str, err    encode to JSON
//
// Supported event kinds (EventKind constants):
//
//	session_start, input, before_agent_start,
//	tool_call, tool_result, turn_end, session_shutdown
//
// Event handlers receive a single Lua table populated from Event.Data.
// Return values are interpreted as follows:
//   - tool_call handler: return false to block the call
//   - input / tool_result handler: return a string to replace the value
//   - all others: return value ignored
package lua

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	glua "github.com/yuin/gopher-lua"
)

// ── event types ───────────────────────────────────────────────────────────────

// EventKind identifies a lifecycle event.
type EventKind string

const (
	EventSessionStart    EventKind = "session_start"
	EventInput          EventKind = "input"
	EventBeforeAgent    EventKind = "before_agent_start"
	EventToolCall       EventKind = "tool_call"
	EventToolResult     EventKind = "tool_result"
	EventTurnEnd        EventKind = "turn_end"
	EventSessionShutdown EventKind = "session_shutdown"
)

// Event is dispatched to all handlers registered for its Kind.
type Event struct {
	Kind EventKind
	Data map[string]any
}

// EventResult carries optional modifications produced by handlers.
type EventResult struct {
	Block    bool   // set by tool_call handler returning false
	Modified string // set by input/tool_result handler returning a string
	HasMod   bool
}

// StatusUpdate is sent on the status channel when an extension calls set_status.
type StatusUpdate struct {
	ID   string
	Text string // empty string means "clear"
}

// Command is a custom slash command registered by an extension.
type Command struct {
	Name string
	Desc string
}

// ── internal types ────────────────────────────────────────────────────────────

type extState struct {
	name  string
	state *glua.LState
	mu    sync.Mutex // serialise all calls into this state
}

type registeredHandler struct {
	ext *extState
	fn  *glua.LFunction
}

type registeredCommand struct {
	desc string
	ext  *extState
	fn   *glua.LFunction
}

// ── Runtime ───────────────────────────────────────────────────────────────────

// Runtime manages all loaded Lua extensions.
type Runtime struct {
	mu       sync.Mutex
	exts     []*extState
	handlers map[EventKind][]registeredHandler
	commands map[string]registeredCommand
	statusCh chan<- StatusUpdate
}

// NewRuntime creates a Runtime. statusCh receives every set_status call;
// pass nil to disable status updates.
func NewRuntime(statusCh chan<- StatusUpdate) *Runtime {
	return &Runtime{
		handlers: make(map[EventKind][]registeredHandler),
		commands: make(map[string]registeredCommand),
		statusCh: statusCh,
	}
}

// Load compiles and executes a Lua extension from path.
// The extension's top-level code runs immediately (registration phase).
func (r *Runtime) Load(name, path string) error {
	ext := &extState{name: name, state: glua.NewState()}
	r.registerAPI(ext)
	if err := ext.state.DoFile(path); err != nil {
		ext.state.Close()
		return fmt.Errorf("extension %s: %w", name, err)
	}
	r.mu.Lock()
	r.exts = append(r.exts, ext)
	r.mu.Unlock()
	return nil
}

// LoadString compiles and executes Lua source from a string (for tests).
func (r *Runtime) LoadString(name, src string) error {
	ext := &extState{name: name, state: glua.NewState()}
	r.registerAPI(ext)
	if err := ext.state.DoString(src); err != nil {
		ext.state.Close()
		return fmt.Errorf("extension %s: %w", name, err)
	}
	r.mu.Lock()
	r.exts = append(r.exts, ext)
	r.mu.Unlock()
	return nil
}

// Fire dispatches an event to all registered handlers for its Kind.
// All handlers run to completion even if one errors; errors are returned
// aggregated but non-fatal (caller may log them).
func (r *Runtime) Fire(event Event) (EventResult, []error) {
	r.mu.Lock()
	handlers := append([]registeredHandler{}, r.handlers[event.Kind]...)
	r.mu.Unlock()

	var result EventResult
	var errs []error

	for _, h := range handlers {
		h.ext.mu.Lock()
		tbl := goMapToLua(h.ext.state, event.Data)
		err := h.ext.state.CallByParam(glua.P{
			Fn:      h.fn,
			NRet:    1,
			Protect: true,
		}, tbl)
		var ret glua.LValue
		if err == nil {
			ret = h.ext.state.Get(-1)
			h.ext.state.Pop(1)
		}
		h.ext.mu.Unlock()

		if err != nil {
			errs = append(errs, fmt.Errorf("%s/%s: %w", h.ext.name, event.Kind, err))
			continue
		}
		switch v := ret.(type) {
		case glua.LBool:
			if !bool(v) {
				result.Block = true
			}
		case glua.LString:
			result.Modified = string(v)
			result.HasMod = true
		}
	}
	return result, errs
}

// FireCommand executes a custom slash command registered by an extension.
// args is the raw argument string after the command name.
func (r *Runtime) FireCommand(name, args string) error {
	r.mu.Lock()
	cmd, ok := r.commands[name]
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown command: %s", name)
	}

	cmd.ext.mu.Lock()
	defer cmd.ext.mu.Unlock()

	argTbl := cmd.ext.state.NewTable()
	cmd.ext.state.RawSetInt(argTbl, 1, glua.LString(args))
	return cmd.ext.state.CallByParam(glua.P{
		Fn:      cmd.fn,
		NRet:    0,
		Protect: true,
	}, argTbl)
}

// HasCommand reports whether a custom command is registered under name.
func (r *Runtime) HasCommand(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.commands[name]
	return ok
}

// ListCommands returns all registered custom commands, sorted by name.
func (r *Runtime) ListCommands() []Command {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Command, 0, len(r.commands))
	for name, cmd := range r.commands {
		out = append(out, Command{Name: name, Desc: cmd.desc})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Close shuts down all Lua states and releases resources.
func (r *Runtime) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ext := range r.exts {
		ext.mu.Lock()
		ext.state.Close()
		ext.mu.Unlock()
	}
	r.exts = nil
}

// ── API registration ──────────────────────────────────────────────────────────

func (r *Runtime) registerAPI(ext *extState) {
	L := ext.state
	mod := L.NewTable()
	L.SetField(mod, "on", L.NewFunction(r.makeApiOn(ext)))
	L.SetField(mod, "register_command", L.NewFunction(r.makeApiRegisterCommand(ext)))
	L.SetField(mod, "set_status", L.NewFunction(r.apiSetStatus))
	L.SetField(mod, "env", L.NewFunction(apiEnv))
	L.SetField(mod, "http_get", L.NewFunction(apiHttpGet))
	L.SetField(mod, "json_decode", L.NewFunction(apiJsonDecode))
	L.SetField(mod, "json_encode", L.NewFunction(apiJsonEncode))
	L.SetGlobal("pigeon", mod)
}

func (r *Runtime) makeApiOn(ext *extState) glua.LGFunction {
	return func(L *glua.LState) int {
		eventName := L.CheckString(1)
		fn := L.CheckFunction(2)
		kind := EventKind(eventName)
		r.mu.Lock()
		r.handlers[kind] = append(r.handlers[kind], registeredHandler{ext: ext, fn: fn})
		r.mu.Unlock()
		return 0
	}
}

func (r *Runtime) makeApiRegisterCommand(ext *extState) glua.LGFunction {
	return func(L *glua.LState) int {
		name := L.CheckString(1)
		desc := L.CheckString(2)
		fn := L.CheckFunction(3)
		r.mu.Lock()
		r.commands[name] = registeredCommand{desc: desc, ext: ext, fn: fn}
		r.mu.Unlock()
		return 0
	}
}

func (r *Runtime) apiSetStatus(L *glua.LState) int {
	id := L.CheckString(1)
	text := ""
	if lv := L.Get(2); lv != glua.LNil {
		text = L.CheckString(2)
	}
	if r.statusCh != nil {
		select {
		case r.statusCh <- StatusUpdate{ID: id, Text: text}:
		default: // drop if full; TUI is too slow
		}
	}
	return 0
}

// ── standalone API functions ──────────────────────────────────────────────────

func apiEnv(L *glua.LState) int {
	name := L.CheckString(1)
	val := os.Getenv(name)
	if val == "" {
		L.Push(glua.LNil)
	} else {
		L.Push(glua.LString(val))
	}
	return 1
}

var sharedHTTPClient = &http.Client{Timeout: 15 * time.Second}

func apiHttpGet(L *glua.LState) int {
	rawURL := L.CheckString(1)
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		L.Push(glua.LNil)
		L.Push(glua.LString(err.Error()))
		return 2
	}
	if hdrs, ok := L.Get(2).(*glua.LTable); ok {
		hdrs.ForEach(func(k, v glua.LValue) {
			req.Header.Set(k.String(), v.String())
		})
	}
	resp, err := sharedHTTPClient.Do(req)
	if err != nil {
		L.Push(glua.LNil)
		L.Push(glua.LString(err.Error()))
		return 2
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		L.Push(glua.LNil)
		L.Push(glua.LString(err.Error()))
		return 2
	}
	L.Push(glua.LString(body))
	L.Push(glua.LNil)
	return 2
}

func apiJsonDecode(L *glua.LState) int {
	s := L.CheckString(1)
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		L.Push(glua.LNil)
		L.Push(glua.LString(err.Error()))
		return 2
	}
	L.Push(goValueToLua(L, v))
	L.Push(glua.LNil)
	return 2
}

func apiJsonEncode(L *glua.LState) int {
	b, err := json.Marshal(luaValueToGo(L.Get(1)))
	if err != nil {
		L.Push(glua.LNil)
		L.Push(glua.LString(err.Error()))
		return 2
	}
	L.Push(glua.LString(b))
	L.Push(glua.LNil)
	return 2
}

// ── value conversions ─────────────────────────────────────────────────────────

func goMapToLua(L *glua.LState, m map[string]any) *glua.LTable {
	tbl := L.NewTable()
	for k, v := range m {
		L.SetField(tbl, k, goValueToLua(L, v))
	}
	return tbl
}

func goValueToLua(L *glua.LState, v any) glua.LValue {
	if v == nil {
		return glua.LNil
	}
	switch val := v.(type) {
	case bool:
		return glua.LBool(val)
	case float64:
		return glua.LNumber(val)
	case int:
		return glua.LNumber(val)
	case string:
		return glua.LString(val)
	case []any:
		tbl := L.NewTable()
		for i, item := range val {
			L.RawSetInt(tbl, i+1, goValueToLua(L, item))
		}
		return tbl
	case map[string]any:
		tbl := L.NewTable()
		for k, item := range val {
			L.SetField(tbl, k, goValueToLua(L, item))
		}
		return tbl
	default:
		return glua.LString(fmt.Sprintf("%v", v))
	}
}

func luaValueToGo(v glua.LValue) any {
	switch val := v.(type) {
	case *glua.LNilType:
		return nil
	case glua.LBool:
		return bool(val)
	case glua.LNumber:
		return float64(val)
	case glua.LString:
		return string(val)
	case *glua.LTable:
		// heuristic: if keys are 1..n integers, treat as array
		n := val.Len()
		if n > 0 {
			allInt := true
			val.ForEach(func(k, _ glua.LValue) {
				if _, ok := k.(glua.LNumber); !ok {
					allInt = false
				}
			})
			if allInt {
				arr := make([]any, n)
				for i := 1; i <= n; i++ {
					arr[i-1] = luaValueToGo(val.RawGetInt(i))
				}
				return arr
			}
		}
		m := make(map[string]any)
		val.ForEach(func(k, v glua.LValue) {
			m[k.String()] = luaValueToGo(v)
		})
		return m
	default:
		return v.String()
	}
}
