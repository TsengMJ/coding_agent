// Package llm defines chat messages and the chat-model abstraction.
//
// It mirrors the subset of `langchain_core.messages` and
// `langchain_core.language_models.BaseChatModel` that the agent relies on.
package llm

import "strings"

// Role identifies who authored a message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// BlockType identifies the kind of a content block.
type BlockType string

const (
	BlockText      BlockType = "text"
	BlockImage     BlockType = "image"
	BlockAudio     BlockType = "audio"
	BlockVideo     BlockType = "video"
	BlockFile      BlockType = "file"
	BlockReasoning BlockType = "reasoning"
)

// ContentBlock is one multimodal piece of a message (langchain `ContentBlock`).
type ContentBlock struct {
	Type BlockType `json:"type"`
	Text string    `json:"text,omitempty"`
	// MimeType applies to binary blocks (image/audio/video/file).
	MimeType string `json:"mime_type,omitempty"`
	// Base64 holds inline binary data; URL references remote or data-URL content.
	Base64 string `json:"base64,omitempty"`
	URL    string `json:"url,omitempty"`
	// CacheControl carries provider prompt-cache markers (e.g. Anthropic breakpoints).
	CacheControl map[string]any `json:"cache_control,omitempty"`
	// Extra holds provider-specific fields that have no dedicated slot.
	Extra map[string]any `json:"extra,omitempty"`
}

// ToolCall is a model's request to invoke a tool.
type ToolCall struct {
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

// Usage reports token consumption for one model call.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
	CacheRead    int `json:"cache_read,omitempty"`
	CacheWrite   int `json:"cache_write,omitempty"`
}

// ToolStatus reports whether a tool message represents success or failure.
type ToolStatus string

const (
	ToolStatusSuccess ToolStatus = "success"
	ToolStatusError   ToolStatus = "error"
)

// Message is a single chat message (System/Human/AI/Tool message in langchain).
type Message struct {
	ID      string         `json:"id,omitempty"`
	Role    Role           `json:"role"`
	Name    string         `json:"name,omitempty"`
	Content []ContentBlock `json:"content"`
	// ToolCalls is set on assistant messages that request tool execution.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID and Status are set on tool messages.
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Status     ToolStatus `json:"status,omitempty"`
	Usage      *Usage     `json:"usage,omitempty"`
	// Metadata mirrors langchain `additional_kwargs` / `response_metadata`.
	Metadata map[string]any `json:"metadata,omitempty"`
}

// TextBlock builds a text content block.
func TextBlock(text string) ContentBlock {
	return ContentBlock{Type: BlockText, Text: text}
}

// NewSystemMessage builds a system message with a single text block.
func NewSystemMessage(text string) Message {
	return Message{Role: RoleSystem, Content: []ContentBlock{TextBlock(text)}}
}

// NewUserMessage builds a user message with a single text block.
func NewUserMessage(text string) Message {
	return Message{Role: RoleUser, Content: []ContentBlock{TextBlock(text)}}
}

// NewAssistantMessage builds an assistant message with a single text block.
func NewAssistantMessage(text string) Message {
	return Message{Role: RoleAssistant, Content: []ContentBlock{TextBlock(text)}}
}

// Text concatenates the text blocks of the message.
func (m Message) Text() string {
	var b strings.Builder
	for _, c := range m.Content {
		if c.Type == BlockText {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

// NewToolMessage builds a tool-result message answering toolCallID.
func NewToolMessage(toolCallID, name, text string) Message {
	return Message{
		Role:       RoleTool,
		Name:       name,
		ToolCallID: toolCallID,
		Status:     ToolStatusSuccess,
		Content:    []ContentBlock{TextBlock(text)},
	}
}
