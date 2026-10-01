package llm

import "strings"

// contextWindows lists context windows (tokens) of common models by model
// name prefix; the longest matching prefix wins. OpenRouter slugs
// ("vendor/name") are matched on the part after the slash. Models not
// listed keep an empty profile, so callers fall back to fixed limits.
var contextWindows = map[string]int{
	"gpt-3.5-turbo":  16385,
	"gpt-4":          8192,
	"gpt-4-turbo":    128000,
	"gpt-4o":         128000,
	"chatgpt-4o":     128000,
	"gpt-4.1":        1047576,
	"gpt-5":          400000,
	"gpt-5.6-luna":   1050000,
	"gpt-6-luna":     1050000,
	"gpt-oss":        131072,
	"o1":             200000,
	"o3":             200000,
	"o4-mini":        200000,
	"claude-":        200000,
	"gemini-1.5-pro": 2097152,
	"gemini-1.5":     1048576,
	"gemini-2":       1048576,
	"deepseek-":      128000,
	"grok-4":         256000,
	"llama-3.1":      131072,
	"llama-3.3":      131072,
	"mistral-large":  128000,
}

// KnownProfile returns the profile dcode knows for a model name, from the
// built-in context-window table.
func KnownProfile(model string) (ModelProfile, bool) {
	if i := strings.LastIndex(model, "/"); i >= 0 {
		model = model[i+1:]
	}
	best := ""
	for prefix := range contextWindows {
		if strings.HasPrefix(model, prefix) && len(prefix) > len(best) {
			best = prefix
		}
	}
	if best == "" {
		return ModelProfile{}, false
	}
	return ModelProfile{MaxInputTokens: contextWindows[best]}, true
}
