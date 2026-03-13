package app

import (
	"fmt"
	"os"
	"strings"
)

const OpenRouterAPIKeyEnv = "OPENROUTER_API_KEY"

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
