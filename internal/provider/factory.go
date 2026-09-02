package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/openai"

	"github.com/nalanj/aadc/internal/provider/assets"
)

// Factory creates fantasy agents from config using catwalk provider definitions
type Factory struct {
	providers map[catwalk.InferenceProvider]catwalk.Provider
}

// NewFactory creates a new provider factory
func NewFactory() *Factory {
	f := &Factory{
		providers: make(map[catwalk.InferenceProvider]catwalk.Provider),
	}
	f.loadProviders()
	return f
}

// loadProviders loads all providers from embedded catwalk configs
func (f *Factory) loadProviders() {
	var providers []catwalk.Provider
	if err := json.Unmarshal(assets.ProvidersJSON, &providers); err != nil {
		return
	}

	for _, p := range providers {
		if p.ID != "" {
			f.providers[p.ID] = p
		}
	}
}

// resolveEnvVar resolves $VAR or ${VAR} patterns
func resolveEnvVar(s string) string {
	re := regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?`)
	return re.ReplaceAllStringFunc(s, func(match string) string {
		varName := match[1:]
		if len(match) > 1 && match[1] == '{' {
			varName = match[2 : len(match)-1]
		}
		return os.Getenv(varName)
	})
}

// CreateProvider creates a fantasy provider from agent config
func (f *Factory) CreateProvider(providerName, modelName string) (fantasy.LanguageModel, error) {
	ctx := context.Background()

	providerID := catwalk.InferenceProvider(providerName)
	providerConfig, ok := f.providers[providerID]
	if !ok {
		return nil, fmt.Errorf("unknown provider: %s (available: %v)", providerName, f.availableProviders())
	}

	apiKey := resolveEnvVar(providerConfig.APIKey)

	if modelName == "" {
		modelName = providerConfig.DefaultLargeModelID
		if modelName == "" {
			modelName = providerConfig.DefaultSmallModelID
		}
	} else {
		// Try to find matching model and use canonical ID
		for _, m := range providerConfig.Models {
			if strings.EqualFold(m.ID, modelName) || strings.EqualFold(m.Name, modelName) {
				modelName = m.ID
				break
			}
		}
	}

	switch providerConfig.Type {
	case catwalk.TypeAnthropic:
		return f.createAnthropicProvider(ctx, providerConfig, apiKey, modelName)
	default:
		return f.createOpenAIProvider(ctx, providerConfig, apiKey, modelName)
	}
}

func (f *Factory) createAnthropicProvider(ctx context.Context, config catwalk.Provider, apiKey, modelName string) (fantasy.LanguageModel, error) {
	baseURL := strings.TrimSuffix(config.APIEndpoint, "/")
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}

	opts := []anthropic.Option{
		anthropic.WithAPIKey(apiKey),
		anthropic.WithBaseURL(baseURL),
	}

	// MiniMax uses X-Api-Key header instead of Authorization: Bearer
	if config.ID == "minimax" || config.ID == "minimax-china" {
		opts = append(opts, anthropic.WithHeaders(map[string]string{"X-Api-Key": apiKey}))
	}

	provider, err := anthropic.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("anthropic provider: %w", err)
	}
	return provider.LanguageModel(ctx, modelName)
}

func (f *Factory) createOpenAIProvider(ctx context.Context, config catwalk.Provider, apiKey, modelName string) (fantasy.LanguageModel, error) {
	baseURL := strings.TrimSuffix(config.APIEndpoint, "/")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}

	provider, err := openai.New(
		openai.WithAPIKey(apiKey),
		openai.WithBaseURL(baseURL),
	)
	if err != nil {
		return nil, fmt.Errorf("openai provider: %w", err)
	}
	return provider.LanguageModel(ctx, modelName)
}

func (f *Factory) availableProviders() []string {
	ids := make([]string, 0, len(f.providers))
	for id := range f.providers {
		ids = append(ids, string(id))
	}
	return ids
}

// ProviderInfo returns information about a provider
type ProviderInfo struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Type   string   `json:"type"`
	Models []string `json:"models"`
}

// ListProviders returns all available providers
func (f *Factory) ListProviders() []ProviderInfo {
	infos := make([]ProviderInfo, 0, len(f.providers))
	for _, p := range f.providers {
		models := make([]string, len(p.Models))
		for i, m := range p.Models {
			models[i] = m.ID
		}
		infos = append(infos, ProviderInfo{
			ID:     string(p.ID),
			Name:   p.Name,
			Type:   string(p.Type),
			Models: models,
		})
	}
	return infos
}
