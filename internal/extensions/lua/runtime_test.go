package lua_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	luaext "pigeon/internal/extensions/lua"
)

func newRuntime(t *testing.T) (*luaext.Runtime, chan luaext.StatusUpdate) {
	t.Helper()
	ch := make(chan luaext.StatusUpdate, 16)
	rt := luaext.NewRuntime(ch)
	t.Cleanup(rt.Close)
	return rt, ch
}

// ── event dispatch ─────────────────────────────────────────────────────────────

func TestFire_HandlerCalled(t *testing.T) {
	rt, ch := newRuntime(t)

	err := rt.LoadString("test", `
		local called = false
		pigeon.on("session_start", function(ev)
			pigeon.set_status("test", "fired")
		end)
	`)
	if err != nil {
		t.Fatalf("LoadString: %v", err)
	}

	rt.Fire(luaext.Event{Kind: luaext.EventSessionStart})

	select {
	case upd := <-ch:
		if upd.ID != "test" || upd.Text != "fired" {
			t.Errorf("unexpected status: %+v", upd)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for status update")
	}
}

func TestFire_EventDataPassedToHandler(t *testing.T) {
	rt, ch := newRuntime(t)

	err := rt.LoadString("test", `
		pigeon.on("tool_call", function(ev)
			pigeon.set_status("tool", ev.name)
		end)
	`)
	if err != nil {
		t.Fatalf("LoadString: %v", err)
	}

	rt.Fire(luaext.Event{
		Kind: luaext.EventToolCall,
		Data: map[string]any{"name": "bash"},
	})

	select {
	case upd := <-ch:
		if upd.Text != "bash" {
			t.Errorf("expected tool name 'bash', got %q", upd.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestFire_BlockReturnedWhenHandlerReturnsFalse(t *testing.T) {
	rt, _ := newRuntime(t)

	err := rt.LoadString("blocker", `
		pigeon.on("tool_call", function(ev)
			return false
		end)
	`)
	if err != nil {
		t.Fatalf("LoadString: %v", err)
	}

	result, _ := rt.Fire(luaext.Event{Kind: luaext.EventToolCall})
	if !result.Block {
		t.Error("expected Block=true when handler returns false")
	}
}

func TestFire_ModifiedReturnedWhenHandlerReturnsString(t *testing.T) {
	rt, _ := newRuntime(t)

	err := rt.LoadString("modifier", `
		pigeon.on("input", function(ev)
			return "modified input"
		end)
	`)
	if err != nil {
		t.Fatalf("LoadString: %v", err)
	}

	result, _ := rt.Fire(luaext.Event{Kind: luaext.EventInput})
	if !result.HasMod || result.Modified != "modified input" {
		t.Errorf("expected Modified='modified input', got %+v", result)
	}
}

func TestFire_MultipleHandlers(t *testing.T) {
	rt, ch := newRuntime(t)

	if err := rt.LoadString("ext1", `pigeon.on("turn_end", function() pigeon.set_status("a", "1") end)`); err != nil {
		t.Fatal(err)
	}
	if err := rt.LoadString("ext2", `pigeon.on("turn_end", function() pigeon.set_status("b", "2") end)`); err != nil {
		t.Fatal(err)
	}

	rt.Fire(luaext.Event{Kind: luaext.EventTurnEnd})

	got := map[string]string{}
	deadline := time.After(time.Second)
	for len(got) < 2 {
		select {
		case upd := <-ch:
			got[upd.ID] = upd.Text
		case <-deadline:
			t.Fatalf("timeout; got %v", got)
		}
	}
	if got["a"] != "1" || got["b"] != "2" {
		t.Errorf("unexpected statuses: %v", got)
	}
}

// ── set_status ─────────────────────────────────────────────────────────────────

func TestSetStatus_ClearWithNil(t *testing.T) {
	rt, ch := newRuntime(t)

	err := rt.LoadString("test", `
		pigeon.on("session_start", function()
			pigeon.set_status("x", nil)
		end)
	`)
	if err != nil {
		t.Fatalf("LoadString: %v", err)
	}

	rt.Fire(luaext.Event{Kind: luaext.EventSessionStart})

	select {
	case upd := <-ch:
		if upd.Text != "" {
			t.Errorf("nil should send empty text, got %q", upd.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

// ── custom commands ────────────────────────────────────────────────────────────

func TestRegisterCommand_ListedAndFireable(t *testing.T) {
	rt, ch := newRuntime(t)

	err := rt.LoadString("test", `
		pigeon.register_command("hello", "say hello", function(args)
			pigeon.set_status("cmd", "hello fired")
		end)
	`)
	if err != nil {
		t.Fatalf("LoadString: %v", err)
	}

	cmds := rt.ListCommands()
	if len(cmds) != 1 || cmds[0].Name != "hello" {
		t.Fatalf("expected command 'hello', got %v", cmds)
	}
	if cmds[0].Desc != "say hello" {
		t.Errorf("unexpected desc: %q", cmds[0].Desc)
	}

	if err := rt.FireCommand("hello", ""); err != nil {
		t.Fatalf("FireCommand: %v", err)
	}

	select {
	case upd := <-ch:
		if upd.Text != "hello fired" {
			t.Errorf("unexpected status: %q", upd.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestFireCommand_UnknownReturnsError(t *testing.T) {
	rt, _ := newRuntime(t)
	if err := rt.FireCommand("nope", ""); err == nil {
		t.Error("expected error for unknown command")
	}
}

func TestHasCommand(t *testing.T) {
	rt, _ := newRuntime(t)
	rt.LoadString("t", `pigeon.register_command("x", "d", function() end)`)
	if !rt.HasCommand("x") {
		t.Error("expected HasCommand('x') = true")
	}
	if rt.HasCommand("y") {
		t.Error("expected HasCommand('y') = false")
	}
}

// ── pigeon.env ─────────────────────────────────────────────────────────────────

func TestEnv_ReturnsValue(t *testing.T) {
	t.Setenv("PIGEON_TEST_VAR", "hello")
	rt, ch := newRuntime(t)

	err := rt.LoadString("test", `
		pigeon.on("session_start", function()
			local v = pigeon.env("PIGEON_TEST_VAR")
			pigeon.set_status("env", v or "nil")
		end)
	`)
	if err != nil {
		t.Fatalf("LoadString: %v", err)
	}

	rt.Fire(luaext.Event{Kind: luaext.EventSessionStart})

	select {
	case upd := <-ch:
		if upd.Text != "hello" {
			t.Errorf("expected 'hello', got %q", upd.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestEnv_MissingReturnsNil(t *testing.T) {
	rt, ch := newRuntime(t)

	err := rt.LoadString("test", `
		pigeon.on("session_start", function()
			local v = pigeon.env("PIGEON_DEFINITELY_NOT_SET_XYZ")
			pigeon.set_status("env", v or "was-nil")
		end)
	`)
	if err != nil {
		t.Fatalf("LoadString: %v", err)
	}
	rt.Fire(luaext.Event{Kind: luaext.EventSessionStart})
	select {
	case upd := <-ch:
		if upd.Text != "was-nil" {
			t.Errorf("expected 'was-nil', got %q", upd.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

// ── json ───────────────────────────────────────────────────────────────────────

func TestJsonDecode_Object(t *testing.T) {
	rt, ch := newRuntime(t)

	err := rt.LoadString("test", `
		pigeon.on("session_start", function()
			local obj, err = pigeon.json_decode('{"name":"pigeon","count":3}')
			if err then
				pigeon.set_status("json", "error: " .. err)
			else
				pigeon.set_status("json", obj.name .. ":" .. tostring(obj.count))
			end
		end)
	`)
	if err != nil {
		t.Fatalf("LoadString: %v", err)
	}

	rt.Fire(luaext.Event{Kind: luaext.EventSessionStart})
	select {
	case upd := <-ch:
		if upd.Text != "pigeon:3" {
			t.Errorf("unexpected json decode result: %q", upd.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestJsonDecode_Invalid(t *testing.T) {
	rt, ch := newRuntime(t)

	err := rt.LoadString("test", `
		pigeon.on("session_start", function()
			local obj, err = pigeon.json_decode("not json")
			pigeon.set_status("json", err and "got-error" or "no-error")
		end)
	`)
	if err != nil {
		t.Fatalf("LoadString: %v", err)
	}

	rt.Fire(luaext.Event{Kind: luaext.EventSessionStart})
	select {
	case upd := <-ch:
		if upd.Text != "got-error" {
			t.Errorf("expected error for invalid JSON, got %q", upd.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestJsonEncode(t *testing.T) {
	rt, ch := newRuntime(t)

	err := rt.LoadString("test", `
		pigeon.on("session_start", function()
			local s, err = pigeon.json_encode({key = "val"})
			pigeon.set_status("json", s or err)
		end)
	`)
	if err != nil {
		t.Fatalf("LoadString: %v", err)
	}

	rt.Fire(luaext.Event{Kind: luaext.EventSessionStart})
	select {
	case upd := <-ch:
		if upd.Text == "" || upd.Text == "err" {
			t.Errorf("expected JSON string, got %q", upd.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

// ── isolation ─────────────────────────────────────────────────────────────────

func TestLoad_ErrorDoesNotAffectOtherExtensions(t *testing.T) {
	rt, ch := newRuntime(t)

	// broken extension
	_ = rt.LoadString("broken", `this is not valid lua %%%`)

	// good extension loaded after the broken one
	err := rt.LoadString("good", `
		pigeon.on("session_start", function()
			pigeon.set_status("good", "ok")
		end)
	`)
	if err != nil {
		t.Fatalf("good extension failed to load: %v", err)
	}

	rt.Fire(luaext.Event{Kind: luaext.EventSessionStart})
	select {
	case upd := <-ch:
		if upd.Text != "ok" {
			t.Errorf("expected 'ok', got %q", upd.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

// ── Load from file ────────────────────────────────────────────────────────────

func TestLoad_FromFile(t *testing.T) {
	rt, ch := newRuntime(t)

	f, err := os.CreateTemp(t.TempDir(), "*.lua")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.WriteString(`pigeon.on("session_start", function() pigeon.set_status("file", "loaded") end)`)
	f.Close()

	if err := rt.Load("fileext", f.Name()); err != nil {
		t.Fatalf("Load: %v", err)
	}

	rt.Fire(luaext.Event{Kind: luaext.EventSessionStart})
	select {
	case upd := <-ch:
		if upd.Text != "loaded" {
			t.Errorf("expected 'loaded', got %q", upd.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestLoad_BadFilePath(t *testing.T) {
	rt, _ := newRuntime(t)
	err := rt.Load("missing", "/nonexistent/path/ext.lua")
	if err == nil {
		t.Error("expected error loading nonexistent file")
	}
}

// ── http_get ──────────────────────────────────────────────────────────────────

func TestHttpGet_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	rt, ch := newRuntime(t)
	script := `
		pigeon.on("session_start", function()
			local body, err = pigeon.http_get("` + srv.URL + `", {})
			pigeon.set_status("http", body or ("err:"..tostring(err)))
		end)
	`
	if err := rt.LoadString("test", script); err != nil {
		t.Fatal(err)
	}
	rt.Fire(luaext.Event{Kind: luaext.EventSessionStart})
	select {
	case upd := <-ch:
		if upd.Text != `{"ok":true}` {
			t.Errorf("unexpected body: %q", upd.Text)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}
}

func TestHttpGet_BadURL(t *testing.T) {
	rt, ch := newRuntime(t)
	script := `
		pigeon.on("session_start", function()
			local body, err = pigeon.http_get("http://127.0.0.1:1", {})
			pigeon.set_status("http", err and "got-error" or "no-error")
		end)
	`
	if err := rt.LoadString("test", script); err != nil {
		t.Fatal(err)
	}
	rt.Fire(luaext.Event{Kind: luaext.EventSessionStart})
	select {
	case upd := <-ch:
		if upd.Text != "got-error" {
			t.Errorf("expected error for bad URL, got %q", upd.Text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}
}

// ── type conversion edge cases ────────────────────────────────────────────────

func TestJsonDecode_Array(t *testing.T) {
	rt, ch := newRuntime(t)
	err := rt.LoadString("test", `
		pigeon.on("session_start", function()
			local arr, _ = pigeon.json_decode('[1,2,3]')
			pigeon.set_status("arr", tostring(arr[2]))
		end)
	`)
	if err != nil {
		t.Fatal(err)
	}
	rt.Fire(luaext.Event{Kind: luaext.EventSessionStart})
	select {
	case upd := <-ch:
		if upd.Text != "2" {
			t.Errorf("expected arr[2]=2, got %q", upd.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestJsonDecode_Nested(t *testing.T) {
	rt, ch := newRuntime(t)
	err := rt.LoadString("test", `
		pigeon.on("session_start", function()
			local obj, _ = pigeon.json_decode('{"data":{"value":42}}')
			pigeon.set_status("nested", tostring(obj.data.value))
		end)
	`)
	if err != nil {
		t.Fatal(err)
	}
	rt.Fire(luaext.Event{Kind: luaext.EventSessionStart})
	select {
	case upd := <-ch:
		if upd.Text != "42" {
			t.Errorf("expected 42, got %q", upd.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestEventData_BoolValue(t *testing.T) {
	rt, ch := newRuntime(t)
	err := rt.LoadString("test", `
		pigeon.on("tool_call", function(ev)
			pigeon.set_status("bool", tostring(ev.allowed))
		end)
	`)
	if err != nil {
		t.Fatal(err)
	}
	rt.Fire(luaext.Event{Kind: luaext.EventToolCall, Data: map[string]any{"allowed": true}})
	select {
	case upd := <-ch:
		if upd.Text != "true" {
			t.Errorf("expected 'true', got %q", upd.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

// ── json_encode ───────────────────────────────────────────────────────────────

func TestAPI_JsonEncode_Table(t *testing.T) {
	rt, ch := newRuntime(t)
	err := rt.LoadString("test", `
		local obj = {key = "value", num = 42}
		local s, err = pigeon.json_encode(obj)
		if err then
			pigeon.set_status("err", err)
		else
			pigeon.set_status("ok", s)
		end
	`)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case upd := <-ch:
		if upd.ID != "ok" {
			t.Errorf("expected ok, got id=%q text=%q", upd.ID, upd.Text)
		}
		if upd.Text == "" {
			t.Error("expected JSON string")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestAPI_JsonEncode_Array(t *testing.T) {
	rt, ch := newRuntime(t)
	err := rt.LoadString("test", `
		local arr = {1, 2, 3}
		local s, _ = pigeon.json_encode(arr)
		pigeon.set_status("ok", s)
	`)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case upd := <-ch:
		if upd.Text != "[1,2,3]" {
			t.Errorf("expected [1,2,3], got %q", upd.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

// ── luaValueToGo table → map ──────────────────────────────────────────────────

func TestAPI_JsonEncode_StringTable(t *testing.T) {
	rt, ch := newRuntime(t)
	err := rt.LoadString("test", `
		local t = {}
		t["x"] = "hello"
		t["y"] = true
		local s, _ = pigeon.json_encode(t)
		pigeon.set_status("ok", s)
	`)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case upd := <-ch:
		if upd.Text == "" {
			t.Error("expected non-empty JSON")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

// ── http_get coverage (via extension code) ────────────────────────────────────

func TestAPI_HttpGet_BadURL(t *testing.T) {
	rt, ch := newRuntime(t)
	err := rt.LoadString("test", `
		local body, err = pigeon.http_get("://bad-url-scheme")
		if err then
			pigeon.set_status("err", err)
		end
	`)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case upd := <-ch:
		if upd.ID != "err" {
			t.Errorf("expected err status, got id=%q text=%q", upd.ID, upd.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for http_get error")
	}
}

// ── ListCommands ──────────────────────────────────────────────────────────────

func TestListCommands_OnlyRegistered(t *testing.T) {
	rt, _ := newRuntime(t)
	err := rt.LoadString("ext", `
		pigeon.register_command("/mytest", "test command", function() end)
	`)
	if err != nil {
		t.Fatal(err)
	}
	cmds := rt.ListCommands()
	found := false
	for _, c := range cmds {
		if c.Name == "/mytest" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected /mytest in commands: %+v", cmds)
	}
}
