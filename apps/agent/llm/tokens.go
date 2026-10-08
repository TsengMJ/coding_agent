package llm

import (
	"encoding/json"
	"errors"
	"math"
	"unicode/utf8"
)

// Parameters of ApproximateTokenCounter (langchain `count_tokens_approximately`).
const (
	charsPerToken         = 4.0
	extraTokensPerMessage = 3.0
	tokensPerImage        = 85
)

// ApproximateTokenCounter is the default character-based token estimate:
// about four characters per token plus a fixed overhead per message, with a
// flat cost per image instead of counting its base64 payload.
func ApproximateTokenCounter(messages []Message, tools []ToolSpec) int {
	total := 0.0
	if len(tools) > 0 {
		chars := 0
		for _, t := range tools {
			chars += jsonLen(t)
		}
		total += math.Ceil(float64(chars) / charsPerToken)
	}
	for _, m := range messages {
		chars := 0
		for _, b := range m.Content {
			switch b.Type {
			case BlockText, BlockReasoning:
				chars += utf8.RuneCountInString(b.Text)
			case BlockImage:
				total += tokensPerImage
			default:
				chars += jsonLen(b)
			}
		}
		if len(m.ToolCalls) > 0 {
			chars += jsonLen(m.ToolCalls)
		}
		chars += len(m.ToolCallID) + len(m.Role) + len(m.Name)
		total += math.Ceil(float64(chars)/charsPerToken) + extraTokensPerMessage
	}
	return int(math.Ceil(total))
}

func jsonLen(v any) int {
	raw, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(raw)
}

// ErrContextOverflow marks a provider rejection because the request exceeds
// the model's context window (langchain `ContextOverflowError`). Callers such
// as summarization match it with errors.Is to compact and retry.
var ErrContextOverflow = errors.New("context window exceeded")
