package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const authFile = "auth.json"

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

// GetAnthropicToken returns the current Anthropic access token, refreshing it
// if expired. It persists the updated credentials on refresh.
// Returns ("", nil) if no Anthropic credentials are stored.
func GetAnthropicToken() (string, error) {
	d, err := Load()
	if err != nil {
		return "", err
	}
	pa, ok := d.Providers["anthropic"]
	if !ok {
		return "", nil
	}
	switch pa.Type {
	case "api_key":
		return pa.Key, nil
	case "oauth":
		if pa.OAuth == nil {
			return "", nil
		}
		if !pa.OAuth.IsExpired() {
			return pa.OAuth.Access, nil
		}
		// Refresh the token.
		fresh, err := Refresh(pa.OAuth.Refresh)
		if err != nil {
			return "", fmt.Errorf("refresh anthropic token: %w", err)
		}
		pa.OAuth = &fresh
		d.Providers["anthropic"] = pa
		if saveErr := Save(d); saveErr != nil {
			// Non-fatal — return the fresh token anyway.
			_ = saveErr
		}
		return fresh.Access, nil
	}
	return "", nil
}

// SetAnthropicAPIKey stores a plain API key for Anthropic.
func SetAnthropicAPIKey(key string) error {
	d, err := Load()
	if err != nil {
		return err
	}
	d.Providers["anthropic"] = ProviderAuth{Type: "api_key", Key: key}
	return Save(d)
}

// SetAnthropicOAuth stores OAuth credentials for Anthropic.
func SetAnthropicOAuth(creds Credentials) error {
	d, err := Load()
	if err != nil {
		return err
	}
	d.Providers["anthropic"] = ProviderAuth{Type: "oauth", OAuth: &creds}
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

func authPath() (string, error) {
	home, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("config dir: %w", err)
	}
	return filepath.Join(home, "pigeon", authFile), nil
}
