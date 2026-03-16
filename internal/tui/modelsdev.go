package tui

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const modelsDevURL = "https://models.dev/api.json"

// modelsDevMsg carries the model-id → context-length map fetched from
// models.dev. A nil map signals that the fetch failed (non-fatal).
type modelsDevMsg struct {
	contextLengths map[string]int
}

// fetchModelsDevContextLengths fetches https://models.dev/api.json and returns
// a flat map of model ID → context window size. The fetch is best-effort: any
// error (network, parse, timeout) results in an empty map so the TUI degrades
// gracefully.
func fetchModelsDevContextLengths() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsDevURL, nil)
		if err != nil {
			return modelsDevMsg{}
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return modelsDevMsg{}
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return modelsDevMsg{}
		}

		// Top-level structure: map of provider-id → provider object.
		// Each provider has a "models" map of model-id → model object.
		// Each model has a "limit" object with a "context" int.
		var payload map[string]struct {
			Models map[string]struct {
				Limit *struct {
					Context int `json:"context"`
				} `json:"limit"`
			} `json:"models"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			return modelsDevMsg{}
		}

		out := make(map[string]int)
		for _, provider := range payload {
			for modelID, model := range provider.Models {
				if model.Limit != nil && model.Limit.Context > 0 {
					out[modelID] = model.Limit.Context
				}
			}
		}
		return modelsDevMsg{contextLengths: out}
	}
}
