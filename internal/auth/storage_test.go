package auth

import (
	"path/filepath"
	"testing"
)

func TestSetAndGetZaiAPIKey(t *testing.T) {
	// Use a temp dir so we don't pollute real config.
	dir := t.TempDir()
	origPath := authPathFn
	authPathFn = func() (string, error) { return filepath.Join(dir, "auth.json"), nil }
	defer func() { authPathFn = origPath }()

	// No key stored yet.
	key, err := GetZaiAPIKey()
	if err != nil {
		t.Fatalf("GetZaiAPIKey (empty): %v", err)
	}
	if key != "" {
		t.Errorf("expected empty key, got %q", key)
	}

	// Store a key.
	if err := SetZaiAPIKey("sk-zai-test123"); err != nil {
		t.Fatalf("SetZaiAPIKey: %v", err)
	}

	// Retrieve it.
	key, err = GetZaiAPIKey()
	if err != nil {
		t.Fatalf("GetZaiAPIKey: %v", err)
	}
	if key != "sk-zai-test123" {
		t.Errorf("got %q, want %q", key, "sk-zai-test123")
	}
}

func TestRemoveZaiProvider(t *testing.T) {
	dir := t.TempDir()
	origPath := authPathFn
	authPathFn = func() (string, error) { return filepath.Join(dir, "auth.json"), nil }
	defer func() { authPathFn = origPath }()

	if err := SetZaiAPIKey("sk-zai-test"); err != nil {
		t.Fatalf("SetZaiAPIKey: %v", err)
	}

	if err := RemoveProvider("zai"); err != nil {
		t.Fatalf("RemoveProvider: %v", err)
	}

	key, err := GetZaiAPIKey()
	if err != nil {
		t.Fatalf("GetZaiAPIKey after remove: %v", err)
	}
	if key != "" {
		t.Errorf("expected empty key after removal, got %q", key)
	}
}

func TestGetZaiAPIKey_otherProviderType(t *testing.T) {
	dir := t.TempDir()
	origPath := authPathFn
	authPathFn = func() (string, error) { return filepath.Join(dir, "auth.json"), nil }
	defer func() { authPathFn = origPath }()

	// Store an oauth-type credential under "zai" — GetZaiAPIKey should return "".
	d, _ := Load()
	d.Providers["zai"] = ProviderAuth{Type: "oauth", OAuth: &Credentials{Access: "token", Refresh: "refresh", Expires: 0}}
	Save(d)

	key, err := GetZaiAPIKey()
	if err != nil {
		t.Fatalf("GetZaiAPIKey: %v", err)
	}
	if key != "" {
		t.Errorf("expected empty for oauth-type, got %q", key)
	}
}
