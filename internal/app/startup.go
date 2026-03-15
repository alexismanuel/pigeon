package app

import (
	"fmt"
	"os"
	"strings"

	"pigeon/internal/auth"
	anthropicclient "pigeon/internal/provider/anthropic"
	"pigeon/internal/provider/lmstudio"
	"pigeon/internal/provider/multi"
	"pigeon/internal/provider/openrouter"
)

const OpenRouterAPIKeyEnv = "OPENROUTER_API_KEY"
const AnthropicAPIKeyEnv = "ANTHROPIC_API_KEY"

// BuildProviders constructs a MultiProvider populated with every provider for
// which credentials are available.  At least one of the following must be
// satisfied or an error is returned:
//
//   - OPENROUTER_API_KEY env var is set
//   - ANTHROPIC_API_KEY env var is set
//   - Anthropic OAuth credentials exist in ~/.config/pigeon/auth.json
//   - LMSTUDIO_BASE_URL is set or LM Studio is reachable at localhost:1234
//
// The returned MultiProvider implements both the StreamingClient and
// modelCatalog interfaces used throughout pigeon.
//
// Provider priority for the default fallback is:
//  1. OpenRouter (if configured)
//  2. Anthropic (if configured)
//  3. LM Studio
func BuildProviders(getenv func(string) string) (*multi.Provider, error) {
	if getenv == nil {
		getenv = os.Getenv
	}

	mp := multi.New()

	// ── OpenRouter ───────────────────────────────────────────────────────────
	if key := strings.TrimSpace(getenv(OpenRouterAPIKeyEnv)); key != "" {
		mp.Add("openrouter", openrouter.NewClient(key, nil))
	}

	// ── Anthropic ────────────────────────────────────────────────────────────
	anthropicKey := strings.TrimSpace(getenv(AnthropicAPIKeyEnv))
	if anthropicKey == "" {
		// Try credentials stored by the OAuth login flow.
		storedKey, err := auth.GetAnthropicToken()
		if err == nil {
			anthropicKey = storedKey
		}
	}
	if anthropicKey != "" {
		mp.Add("anthropic", anthropicclient.NewClient(anthropicKey, nil))
	}

	// ── LM Studio ────────────────────────────────────────────────────────────
	// LM Studio is always registered — it gracefully returns an empty model
	// list when the server is not running.
	mp.Add("lmstudio", lmstudio.NewClient("", "", nil))

	// Require at least one real (non-LM-Studio) provider so that the user
	// gets a clear error message rather than a confusing empty model list.
	if strings.TrimSpace(getenv(OpenRouterAPIKeyEnv)) == "" && anthropicKey == "" {
		// Only LM Studio is available.  That's allowed if LM Studio is
		// configured explicitly.
		if strings.TrimSpace(getenv("LMSTUDIO_BASE_URL")) == "" {
			return nil, fmt.Errorf(
				"no provider configured: set %s, %s, or LMSTUDIO_BASE_URL, "+
					"or run `pigeon login` to authenticate with Anthropic",
				OpenRouterAPIKeyEnv, AnthropicAPIKeyEnv,
			)
		}
	}

	return mp, nil
}

// ResolveOpenRouterAPIKey is kept for backwards compatibility with callers
// that only want the OpenRouter key.
func ResolveOpenRouterAPIKey(getenv func(string) string) (string, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	apiKey := strings.TrimSpace(getenv(OpenRouterAPIKeyEnv))
	if apiKey == "" {
		return "", fmt.Errorf("missing %s: export %s=... and restart pigeon", OpenRouterAPIKeyEnv, OpenRouterAPIKeyEnv)
	}
	return apiKey, nil
}
