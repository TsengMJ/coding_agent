package llm

import (
	"errors"
	"os"
)

// DefaultOpenRouterBaseURL is used when OPENROUTER_API_BASE is not set.
const DefaultOpenRouterBaseURL = "https://openrouter.ai/api/v1"

// Default OpenRouter app attribution (https://openrouter.ai/docs/app-attribution).
const (
	defaultOpenRouterAppURL   = "https://github.com/tsengmingchun/quill"
	defaultOpenRouterAppTitle = "Quill"
)

func init() {
	if err := RegisterProvider("openrouter", newOpenRouterFromSpec); err != nil {
		panic(err)
	}
}

func newOpenRouterFromSpec(model string, opts map[string]any) (ChatModel, error) {
	cfg := ProviderConfig{
		Model:    model,
		APIKey:   os.Getenv("OPENROUTER_API_KEY"),
		BaseURL:  os.Getenv("OPENROUTER_API_BASE"),
		Settings: opts,
	}
	if cfg.APIKey == "" {
		return nil, errors.New("openrouter: OPENROUTER_API_KEY is not set")
	}
	return NewOpenRouterModel(cfg), nil
}

// NewOpenRouterModel builds a Chat Completions client for OpenRouter. Model
// names use OpenRouter slugs such as "openai/gpt-6-luna-pro".
//
// OPENROUTER_APP_URL / OPENROUTER_APP_TITLE override the attribution headers;
// setting either to an empty string omits that header.
func NewOpenRouterModel(cfg ProviderConfig) *OpenAIModel {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultOpenRouterBaseURL
	}
	m := NewOpenAIModel(cfg, false)
	m.provider = "openrouter"
	m.headers = map[string]string{}
	if v := envOr("OPENROUTER_APP_URL", defaultOpenRouterAppURL); v != "" {
		m.headers["HTTP-Referer"] = v
	}
	if v := envOr("OPENROUTER_APP_TITLE", defaultOpenRouterAppTitle); v != "" {
		m.headers["X-Title"] = v
	}
	return m
}

// envOr returns the variable's value when it is set (even if empty) and
// fallback otherwise.
func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}
