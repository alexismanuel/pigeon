package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const authFile = "auth.json"

// Credentials holds an OAuth access + refresh token pair.
type Credentials struct {
	Access  string `json:"access"`
	Refresh string `json:"refresh"`
	// Expires is Unix milliseconds at which the access token becomes invalid.
	Expires int64 `json:"expires"`
}

// IsExpired returns true when the access token is expired or about to expire.
func (c Credentials) IsExpired() bool {
	return c.Expires != 0 && time.Now().UnixMilli() >= c.Expires
}

// ProviderAuth holds credentials for a single provider.
type ProviderAuth struct {
	// Type is "api_key" or "oauth".
	Type string `json:"type"`
	// Key is set when Type == "api_key".
	Key string `json:"key,omitempty"`
	// OAuthCredentials is set when Type == "oauth".
	OAuth *Credentials `json:"oauth,omitempty"`
}

// AuthData is the top-level structure persisted to ~/.config/pigeon/auth.json.
type AuthData struct {
	Providers map[string]ProviderAuth `json:"providers,omitempty"`
}

// Load reads ~/.config/pigeon/auth.json. Missing file returns empty AuthData.
func Load() (AuthData, error) {
	p, err := authPath()
	if err != nil {
		return AuthData{}, err
	}
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return AuthData{Providers: map[string]ProviderAuth{}}, nil
	}
	if err != nil {
		return AuthData{}, fmt.Errorf("read auth file: %w", err)
	}
	var out AuthData
	if err := json.Unmarshal(data, &out); err != nil {
		return AuthData{}, fmt.Errorf("parse auth file: %w", err)
	}
	if out.Providers == nil {
		out.Providers = map[string]ProviderAuth{}
	}
	return out, nil
}

// Save persists auth data to ~/.config/pigeon/auth.json (mode 0600).
func Save(d AuthData) error {
	p, err := authPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal auth data: %w", err)
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		return fmt.Errorf("write auth file: %w", err)
	}
	return nil
}

// GetZaiAPIKey returns the stored Zai API key.
// Returns ("", nil) if no Zai credentials are stored.
func GetZaiAPIKey() (string, error) {
	d, err := Load()
	if err != nil {
		return "", err
	}
	pa, ok := d.Providers["zai"]
	if !ok {
		return "", nil
	}
	if pa.Type == "api_key" {
		return pa.Key, nil
	}
	return "", nil
}

// SetZaiAPIKey stores a plain API key for Zai.
func SetZaiAPIKey(key string) error {
	d, err := Load()
	if err != nil {
		return err
	}
	d.Providers["zai"] = ProviderAuth{Type: "api_key", Key: key}
	return Save(d)
}

// RemoveProvider removes credentials for the given provider.
func RemoveProvider(provider string) error {
	d, err := Load()
	if err != nil {
		return err
	}
	delete(d.Providers, provider)
	return Save(d)
}

// authPathFn returns the path to auth.json. It is a variable so tests can
// override it to use a temp directory.
var authPathFn = defaultAuthPath

func authPath() (string, error) { return authPathFn() }

func defaultAuthPath() (string, error) {
	home, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("config dir: %w", err)
	}
	return filepath.Join(home, "pigeon", authFile), nil
}
