// Package multi implements a multi-provider dispatcher that aggregates model
// catalogs and routes streaming requests to the appropriate backend based on
// the model ID.
//
// Model-to-provider routing rules (evaluated in order):
//  1. Model IDs starting with "claude-" → Anthropic (when registered).
//  2. Models discovered at ListModels time are stored in a lookup table.
//  3. Everything else falls through to the first registered default provider
//     (typically OpenRouter).
package multi

import (
	"context"
	"strings"
	"sync"

	"pigeon/internal/provider/openrouter"
)

// ProviderClient is the interface that each backend must satisfy.
type ProviderClient interface {
	StreamChatCompletion(
		ctx context.Context,
		model string,
		messages []openrouter.Message,
		tools []openrouter.ToolDefinition,
		onEvent openrouter.StreamHandler,
	) (openrouter.Message, error)
	ListModels(ctx context.Context) ([]openrouter.ModelInfo, error)
}

// entry bundles a named provider with its client.
type entry struct {
	id     string
	client ProviderClient
}

// Provider is the multi-provider dispatcher. It implements the same
// ProviderClient interface so it can be dropped in wherever a single provider
// is expected.
type Provider struct {
	providers []entry
	// defaultID is the provider used when no specific routing rule matches.
	defaultID string

	mu       sync.RWMutex
	modelMap map[string]string // model ID → provider ID, built by ListModels
}

// New creates an empty Provider.  At least one provider must be added via Add
// before any other method is called.
func New() *Provider {
	return &Provider{
		modelMap: make(map[string]string),
	}
}

// Add registers a named provider.  The first provider added becomes the
// default fallback.
func (mp *Provider) Add(id string, client ProviderClient) {
	mp.providers = append(mp.providers, entry{id: id, client: client})
	if mp.defaultID == "" {
		mp.defaultID = id
	}
}

// ListModels fetches models from all registered providers concurrently,
// combines the results, and updates the internal model→provider routing table.
// Errors from individual providers are silently ignored so that a failed or
// absent provider (e.g. LM Studio not running) does not block the others.
func (mp *Provider) ListModels(ctx context.Context) ([]openrouter.ModelInfo, error) {
	type result struct {
		providerID string
		models     []openrouter.ModelInfo
	}
	ch := make(chan result, len(mp.providers))

	for _, e := range mp.providers {
		e := e
		go func() {
			models, _ := e.client.ListModels(ctx) // ignore per-provider errors
			ch <- result{providerID: e.id, models: models}
		}()
	}

	newMap := make(map[string]string)
	var all []openrouter.ModelInfo

	for range mp.providers {
		r := <-ch
		for _, m := range r.models {
			newMap[m.ID] = r.providerID
		}
		all = append(all, r.models...)
	}

	mp.mu.Lock()
	mp.modelMap = newMap
	mp.mu.Unlock()

	return all, nil
}

// StreamChatCompletion routes the call to the appropriate provider based on
// the model ID.
func (mp *Provider) StreamChatCompletion(
	ctx context.Context,
	model string,
	messages []openrouter.Message,
	tools []openrouter.ToolDefinition,
	onEvent openrouter.StreamHandler,
) (openrouter.Message, error) {
	client := mp.resolveClient(model)
	return client.StreamChatCompletion(ctx, model, messages, tools, onEvent)
}

// resolveClient returns the ProviderClient responsible for the given model.
func (mp *Provider) resolveClient(model string) ProviderClient {
	// 1. Lookup in the map built by the last ListModels call.
	mp.mu.RLock()
	providerID, ok := mp.modelMap[model]
	mp.mu.RUnlock()
	if ok {
		if c := mp.clientByID(providerID); c != nil {
			return c
		}
	}

	// 2. Static routing heuristics.
	if strings.HasPrefix(model, "claude-") {
		if c := mp.clientByID("anthropic"); c != nil {
			return c
		}
	}

	// 3. Fall back to the default provider.
	if c := mp.clientByID(mp.defaultID); c != nil {
		return c
	}

	// 4. Last resort: use the first registered provider.
	if len(mp.providers) > 0 {
		return mp.providers[0].client
	}
	return nil
}

func (mp *Provider) clientByID(id string) ProviderClient {
	for _, e := range mp.providers {
		if e.id == id {
			return e.client
		}
	}
	return nil
}
