package app

import (
	"fmt"
	"os"
	"strings"

	"pigeon/internal/auth"
	"pigeon/internal/provider/lmstudio"
	"pigeon/internal/provider/multi"
	"pigeon/internal/provider/openrouter"
	zaiclient "pigeon/internal/provider/zai"
)

const OpenRouterAPIKeyEnv = "OPENROUTER_API_KEY"
const ZaiAPIKeyEnv = "ZAI_API_KEY"

// BuildProviders constructs a MultiProvider populated with every provider for
// which credentials are available. At least one of the following must be
// satisfied or an error is returned:
//
//	- OPENROUTER_API_KEY env var is set
//	- ZAI_API_KEY env var is set or Zai credentials exist in auth.json
//	- LMSTUDIO_BASE_URL is set or LM Studio is reachable at localhost:1234
//
// The returned MultiProvider implements both the StreamingClient and
// modelCatalog interfaces used throughout pigeon.
//
// Provider priority for the default fallback is:
//  1. OpenRouter (if configured)
//  2. Zai (if configured)
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

	// ── Zai (Zecoba) ──────────────────────────────────────────────────────────
	zaiKey := strings.TrimSpace(getenv(ZaiAPIKeyEnv))
	if zaiKey == "" {
		// Try credentials stored by the login flow.
		storedKey, err := auth.GetZaiAPIKey()
		if err == nil {
			zaiKey = storedKey
		}
	}
	if zaiKey != "" {
		mp.Add("zai", zaiclient.NewClient(zaiKey, nil))
	}

	// ── LM Studio ────────────────────────────────────────────────────────────
	// LM Studio is always registered — it gracefully returns an empty model
	// list when the server is not running.
	lmStudioAPIKey := strings.TrimSpace(getenv("LMSTUDIO_API_KEY"))
	mp.Add("lmstudio", lmstudio.NewClient("", lmStudioAPIKey, nil))

	// Require at least one real (non-LM-Studio) provider so that the user
	// gets a clear error message rather than a confusing empty model list.
	if strings.TrimSpace(getenv(OpenRouterAPIKeyEnv)) == "" && zaiKey == "" {
		if strings.TrimSpace(getenv("LMSTUDIO_BASE_URL")) == "" && lmStudioAPIKey == "" {
			return nil, fmt.Errorf(
				"no provider configured: set %s, %s, LMSTUDIO_BASE_URL, or LMSTUDIO_API_KEY",
				OpenRouterAPIKeyEnv, ZaiAPIKeyEnv,
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
