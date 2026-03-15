// Package auth handles OAuth credential storage and login flows.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// Anthropic OAuth constants (Claude Pro/Max subscription).
	AnthropicClientID    = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	AnthropicAuthorizeURL = "https://claude.ai/oauth/authorize"
	AnthropicTokenURL    = "https://platform.claude.com/v1/oauth/token"
	anthropicCallbackPort = 53692
	anthropicRedirectURI = "http://localhost:53692/callback"
	anthropicScopes      = "org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"

	// tokenExpiryBuffer is subtracted from the token's expires_in to allow
	// for clock skew and request latency.
	tokenExpiryBuffer = 5 * time.Minute
)

// Credentials holds an OAuth access + refresh token pair.
type Credentials struct {
	Access  string `json:"access"`
	Refresh string `json:"refresh"`
	// Expires is Unix milliseconds at which the access token becomes invalid.
	Expires int64 `json:"expires"`
}

// IsExpired returns true when the access token is expired or about to expire.
func (c Credentials) IsExpired() bool {
	return time.Now().UnixMilli() >= c.Expires
}

// LoginCallbacks provides hooks that the caller must supply to drive the
// interactive OAuth authorization code + PKCE flow.
type LoginCallbacks struct {
	// OnAuthURL is called with the browser URL the user should visit.
	OnAuthURL func(authURL string)
	// OnProgress is called with human-readable status messages.
	OnProgress func(msg string)
}

// Login runs the full OAuth authorization code + PKCE flow for Anthropic.
// It starts a temporary local HTTP server to receive the redirect callback,
// opens (or prints) the authorize URL, and exchanges the code for tokens.
func Login(ctx context.Context, cb LoginCallbacks) (Credentials, error) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return Credentials{}, fmt.Errorf("generate PKCE: %w", err)
	}

	// Channel to receive (code, state) from the callback server.
	type callbackResult struct {
		code  string
		state string
		err   error
	}
	resultCh := make(chan callbackResult, 1)

	// Start local callback server.
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", anthropicCallbackPort))
	if err != nil {
		return Credentials{}, fmt.Errorf("start callback server: %w", err)
	}
	srv := &http.Server{}
	srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		code := q.Get("code")
		state := q.Get("state")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			resultCh <- callbackResult{err: fmt.Errorf("no code in callback")}
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, successHTML)
		resultCh <- callbackResult{code: code, state: state}
	})

	go func() { _ = srv.Serve(listener) }()
	defer srv.Close()

	// Build the authorization URL.
	params := url.Values{
		"code":                  {"true"},
		"client_id":             {AnthropicClientID},
		"response_type":         {"code"},
		"redirect_uri":          {anthropicRedirectURI},
		"scope":                 {anthropicScopes},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {verifier},
	}
	authURL := AnthropicAuthorizeURL + "?" + params.Encode()

	if cb.OnAuthURL != nil {
		cb.OnAuthURL(authURL)
	}

	if cb.OnProgress != nil {
		cb.OnProgress("Waiting for browser callback…")
	}

	// Wait for callback or context cancellation.
	select {
	case <-ctx.Done():
		return Credentials{}, ctx.Err()
	case result := <-resultCh:
		if result.err != nil {
			return Credentials{}, result.err
		}
		if cb.OnProgress != nil {
			cb.OnProgress("Exchanging authorization code…")
		}
		creds, err := exchangeCode(result.code, verifier)
		if err != nil {
			return Credentials{}, err
		}
		return creds, nil
	}
}

// Refresh exchanges a refresh token for a new Credentials pair.
func Refresh(refreshToken string) (Credentials, error) {
	return postTokenJSON(map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     AnthropicClientID,
		"refresh_token": refreshToken,
	})
}

// ─── internal helpers ───────────────────────────────────────────────────────

func exchangeCode(code, verifier string) (Credentials, error) {
	return postTokenJSON(map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     AnthropicClientID,
		"code":          code,
		"state":         verifier,
		"redirect_uri":  anthropicRedirectURI,
		"code_verifier": verifier,
	})
}

func postTokenJSON(body map[string]string) (Credentials, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return Credentials{}, fmt.Errorf("marshal token request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, AnthropicTokenURL, strings.NewReader(string(data)))
	if err != nil {
		return Credentials{}, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return Credentials{}, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()

	var raw struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return Credentials{}, fmt.Errorf("decode token response: %w", err)
	}
	if raw.Error != "" {
		return Credentials{}, fmt.Errorf("token error %q: %s", raw.Error, raw.ErrorDesc)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Credentials{}, fmt.Errorf("token exchange HTTP %d", resp.StatusCode)
	}
	expiresMs := time.Now().Add(time.Duration(raw.ExpiresIn)*time.Second - tokenExpiryBuffer).UnixMilli()
	return Credentials{
		Access:  raw.AccessToken,
		Refresh: raw.RefreshToken,
		Expires: expiresMs,
	}, nil
}

// generatePKCE creates a code verifier and its SHA-256 S256 challenge.
func generatePKCE() (verifier, challenge string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return
}

const successHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8"/>
  <title>Authentication successful</title>
</head>
<body>
  <p>Authentication successful. Return to your terminal to continue.</p>
</body>
</html>`
