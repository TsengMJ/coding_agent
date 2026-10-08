package runtime

import (
	"testing"

	"github.com/tsengmingchun/quill/apps/agent/llm"
)

func msg(id, text string) llm.Message {
	m := llm.NewUserMessage(text)
	m.ID = id
	return m
}

func texts(msgs []llm.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Content[0].Text
	}
	return out
}

func assertTexts(t *testing.T, got []llm.Message, want ...string) {
	t.Helper()
	g := texts(got)
	if len(g) != len(want) {
		t.Fatalf("got %v, want %v", g, want)
	}
	for i := range want {
		if g[i] != want[i] {
			t.Fatalf("got %v, want %v", g, want)
		}
	}
}

func TestMessagesReducerAppends(t *testing.T) {
	got, err := MessagesReducer([]llm.Message{msg("1", "a")}, []llm.Message{msg("2", "b"), msg("", "c")})
	if err != nil {
		t.Fatal(err)
	}
	assertTexts(t, got, "a", "b", "c")
}

func TestMessagesReducerReplacesByID(t *testing.T) {
	got, err := MessagesReducer([]llm.Message{msg("1", "a"), msg("2", "b")}, []llm.Message{msg("1", "A")})
	if err != nil {
		t.Fatal(err)
	}
	assertTexts(t, got, "A", "b")
}

func TestMessagesReducerRemoveAll(t *testing.T) {
	update := []llm.Message{{ID: RemoveAllMessages}, msg("3", "summary")}
	got, err := MessagesReducer([]llm.Message{msg("1", "a"), msg("2", "b")}, update)
	if err != nil {
		t.Fatal(err)
	}
	assertTexts(t, got, "summary")
}

func TestMessagesReducerDoesNotMutateInput(t *testing.T) {
	current := []llm.Message{msg("1", "a")}
	if _, err := MessagesReducer(current, []llm.Message{msg("1", "A")}); err != nil {
		t.Fatal(err)
	}
	assertTexts(t, current, "a")
}

func TestApplyUsesDeclaredReducer(t *testing.T) {
	sum := func(current, update any) (any, error) {
		c, _ := current.(int)
		return c + update.(int), nil
	}
	schema := Schema{"count": {Name: "count", Reducer: sum}}
	s := &State{}
	for range 2 {
		if err := s.Apply(Update{Values: map[string]any{"count": 3}}, schema); err != nil {
			t.Fatal(err)
		}
	}
	if s.Values["count"] != 6 {
		t.Fatalf("count = %v, want 6", s.Values["count"])
	}
}

func TestApplyLastWriteWinsWithoutReducer(t *testing.T) {
	s := &State{Values: map[string]any{"mode": "a"}}
	if err := s.Apply(Update{Values: map[string]any{"mode": "b"}}, nil); err != nil {
		t.Fatal(err)
	}
	if s.Values["mode"] != "b" {
		t.Fatalf("mode = %v, want b", s.Values["mode"])
	}
}

func TestCloneIsIndependentAtTopLevel(t *testing.T) {
	s := &State{Messages: []llm.Message{msg("1", "a")}, Values: map[string]any{"k": 1}}
	c := s.Clone()
	c.Messages[0] = msg("1", "changed")
	c.Values["k"] = 2
	assertTexts(t, s.Messages, "a")
	if s.Values["k"] != 1 {
		t.Fatal("clone must not share the Values map")
	}
}

func TestPrivateKeysCollectsSortedUniqueNames(t *testing.T) {
	got := PrivateKeys(
		Schema{"b": {Name: "b", Private: true}, "files": {Name: "files"}},
		Schema{"a": {Name: "a", Private: true}, "b": {Name: "b", Private: true}},
	)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("PrivateKeys = %v", got)
	}
}
