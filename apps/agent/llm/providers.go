package llm

// ProviderConfig holds common construction options for built-in providers.
type ProviderConfig struct {
	Model   string
	APIKey  string
	BaseURL string
	// Settings are default per-call settings (temperature, max tokens, ...).
	Settings map[string]any
}
