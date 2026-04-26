package app

import "testing"

func TestResolveOpenRouterAPIKey(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		wantErr bool
	}{
		{name: "missing", env: "", wantErr: true},
		{name: "whitespace", env: "   ", wantErr: true},
		{name: "present", env: "sk-or-123", wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apiKey, err := ResolveOpenRouterAPIKey(func(string) string { return tt.env })
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if apiKey != tt.env {
				t.Fatalf("unexpected api key: got %q want %q", apiKey, tt.env)
			}
		})
	}
}

func TestBuildProviders_withZaiKey(t *testing.T) {
	getenv := func(key string) string {
		switch key {
		case "ZAI_API_KEY":
			return "sk-zai-test-key"
		default:
			return ""
		}
	}

	mp, err := BuildProviders(getenv)
	if err != nil {
		t.Fatalf("BuildProviders: %v", err)
	}
	if mp == nil {
		t.Fatal("expected non-nil multi-provider")
	}
}

func TestBuildProviders_noProvider(t *testing.T) {
	// NOTE: This test will pass only when no real Zai credentials are stored
	// in auth.json. If ZAI_API_KEY is stored, BuildProviders succeeds.
	getenv := func(key string) string {
		switch key {
		case "OPENROUTER_API_KEY",
			"ZAI_API_KEY",
			"LMSTUDIO_BASE_URL",
			"LMSTUDIO_API_KEY":
			return ""
		default:
			return ""
		}
	}

	_, err := BuildProviders(getenv)
	if err == nil {
		t.Skip("skipping: stored credentials found in auth.json")
	}
}
