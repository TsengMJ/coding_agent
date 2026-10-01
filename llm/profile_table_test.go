package llm

import "testing"

func TestKnownProfileMatchesLongestPrefix(t *testing.T) {
	cases := map[string]int{
		"gpt-4o":                    128000,
		"gpt-4o-mini-2024-07-18":    128000,
		"gpt-4.1-nano":              1047576,
		"gpt-5.5":                   400000,
		"openai/gpt-6-luna-pro":     1050000,
		"openai/gpt-5.6-luna":       1050000,
		"openai/gpt-4.1":            1047576,
		"anthropic/claude-sonnet-4": 200000,
		"google/gemini-2.5-pro":     1048576,
	}
	for model, want := range cases {
		p, ok := KnownProfile(model)
		if !ok || p.MaxInputTokens != want {
			t.Errorf("%s: got %d (%v), want %d", model, p.MaxInputTokens, ok, want)
		}
	}
}

func TestKnownProfileUnknownModel(t *testing.T) {
	if p, ok := KnownProfile("my-local-model"); ok || p.MaxInputTokens != 0 {
		t.Fatalf("got %+v %v", p, ok)
	}
}

func TestOpenAIModelProfileUsesTable(t *testing.T) {
	m := NewOpenAIModel(ProviderConfig{Model: "gpt-4o"}, false)
	if got := m.Profile().MaxInputTokens; got != 128000 {
		t.Fatalf("MaxInputTokens = %d", got)
	}
	r := NewOpenRouterModel(ProviderConfig{Model: "openai/gpt-4o"})
	if got := r.Profile().MaxInputTokens; got != 128000 {
		t.Fatalf("openrouter MaxInputTokens = %d", got)
	}
}
