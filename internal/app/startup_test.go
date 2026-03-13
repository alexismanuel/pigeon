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
