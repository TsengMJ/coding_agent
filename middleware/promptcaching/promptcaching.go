// Package promptcaching adds provider prompt-cache markers to model requests
// (Anthropic / Bedrock / Fireworks prompt-caching middleware).
package promptcaching

import (
	"context"

	"github.com/tsengmingchun/quill/agent"
	"github.com/tsengmingchun/quill/internal/notimpl"
)

// Provider selects the caching dialect.
type Provider string

const (
	ProviderAnthropic Provider = "anthropic"
	ProviderBedrock   Provider = "bedrock"
	ProviderFireworks Provider = "fireworks"
)

// Middleware marks cache breakpoints; it no-ops for models of other providers.
type Middleware struct {
	provider Provider
}

// New returns a caching middleware for one provider.
func New(p Provider) *Middleware { return &Middleware{provider: p} }

func (m *Middleware) Name() string {
	switch m.provider {
	case ProviderBedrock:
		return "BedrockPromptCachingMiddleware"
	case ProviderFireworks:
		return "FireworksPromptCachingMiddleware"
	default:
		return "AnthropicPromptCachingMiddleware"
	}
}

func (m *Middleware) WrapModelCall(ctx context.Context, req *agent.ModelRequest, next agent.ModelHandler) (*agent.ModelResponse, error) {
	return nil, notimpl.Err("promptcaching.Middleware.WrapModelCall")
}

// AppendDefaults appends every provider's caching middleware to stack
// (`append_prompt_caching_middleware`).
func AppendDefaults(stack []agent.Middleware) []agent.Middleware {
	return append(stack, New(ProviderAnthropic), New(ProviderBedrock), New(ProviderFireworks))
}
