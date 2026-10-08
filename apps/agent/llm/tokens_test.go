package llm

import "testing"

func TestApproximateTokenCounter(t *testing.T) {
	// 16 text chars + 4 role chars = 20 chars -> 5 tokens, plus 3 per message.
	if got := ApproximateTokenCounter([]Message{NewUserMessage("abcdefghijklmnop")}, nil); got != 8 {
		t.Fatalf("got %d, want 8", got)
	}
}

func TestApproximateTokenCounterImageIsFlat(t *testing.T) {
	big := Message{Role: RoleUser, Content: []ContentBlock{{Type: BlockImage, Base64: string(make([]byte, 100000))}}}
	small := Message{Role: RoleUser, Content: []ContentBlock{{Type: BlockImage, Base64: "x"}}}
	if a, b := ApproximateTokenCounter([]Message{big}, nil), ApproximateTokenCounter([]Message{small}, nil); a != b {
		t.Fatalf("image cost depends on payload: %d vs %d", a, b)
	}
}

func TestApproximateTokenCounterCountsToolCallsAndTools(t *testing.T) {
	plain := ApproximateTokenCounter([]Message{{Role: RoleAssistant}}, nil)
	withCall := ApproximateTokenCounter([]Message{{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "write_file", Args: map[string]any{"content": "0123456789"}}}}}, nil)
	withTools := ApproximateTokenCounter([]Message{{Role: RoleAssistant}}, []ToolSpec{{Name: "ls", Description: "list files"}})
	if withCall <= plain || withTools <= plain {
		t.Fatalf("plain=%d withCall=%d withTools=%d", plain, withCall, withTools)
	}
}
