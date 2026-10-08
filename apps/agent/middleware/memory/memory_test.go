package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tsengmingchun/quill/apps/agent/agent"
	"github.com/tsengmingchun/quill/apps/agent/backend"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/runtime"
)

func newMiddleware(t *testing.T, sources ...string) *Middleware {
	t.Helper()
	m, err := New(Options{Backend: backend.NewFilesystemBackend(backend.FilesystemOptions{}), Sources: sources})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func systemText(t *testing.T, model *llm.FakeModel, call int) string {
	t.Helper()
	msgs := model.Requests()[call].Messages
	if len(msgs) == 0 || msgs[0].Role != llm.RoleSystem {
		t.Fatalf("request %d has no system message", call)
	}
	return msgs[0].Text()
}

func TestMemoryIsInjectedAndMissingFilesSkipped(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(present, []byte("use tabs\n<!-- private note -->\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newMiddleware(t, filepath.Join(dir, "missing.md"), present)
	model := llm.NewFakeModel(llm.NewAssistantMessage("ok"))
	a, err := agent.CreateAgent(agent.Config{Model: model, Middleware: []agent.Middleware{m}})
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage("hi")}}, runtime.RunConfig{})
	if err != nil {
		t.Fatal(err)
	}
	sys := systemText(t, model, 0)
	if !strings.Contains(sys, present+"\n\nuse tabs") || strings.Contains(sys, "private note") || strings.Contains(sys, "missing.md") {
		t.Fatalf("system prompt:\n%s", sys)
	}
	if got := contentsFrom(st.Values[ContentsStateKey]); len(got) != 1 {
		t.Fatalf("contents = %v", got)
	}
}

func TestMemoryLoadsOncePerThread(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	model := llm.NewFakeModel(llm.NewAssistantMessage("a"), llm.NewAssistantMessage("b"))
	a, err := agent.CreateAgent(agent.Config{
		Model:        model,
		Middleware:   []agent.Middleware{newMiddleware(t, path)},
		Checkpointer: runtime.NewMemoryCheckpointer(),
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := runtime.RunConfig{ThreadID: "t"}
	for i, text := range []string{"one", "two"} {
		if _, err := a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage(text)}}, cfg); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if err := os.WriteFile(path, []byte("second"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if sys := systemText(t, model, 1); !strings.Contains(sys, "first") || strings.Contains(sys, "second") {
		t.Fatalf("second run must reuse loaded memory:\n%s", sys)
	}
}

func TestMemoryReloadSeesEditsBetweenRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := New(Options{Backend: backend.NewFilesystemBackend(backend.FilesystemOptions{}), Sources: []string{path}, Reload: true})
	if err != nil {
		t.Fatal(err)
	}
	st := &runtime.State{Values: map[string]any{}}
	apply := func() *runtime.Update {
		upd, err := m.BeforeAgent(context.Background(), st, nil)
		if err != nil {
			t.Fatal(err)
		}
		if upd != nil {
			st.Values[ContentsStateKey] = upd.Values[ContentsStateKey]
		}
		return upd
	}
	apply()
	if upd := apply(); upd != nil {
		t.Fatalf("unchanged memory produced an update: %+v", upd)
	}
	if err := os.WriteFile(path, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	if apply() == nil || contentsFrom(st.Values[ContentsStateKey])[path] != "second" {
		t.Fatalf("edit not reloaded: %v", st.Values[ContentsStateKey])
	}
}

func TestNoMemoryPlaceholder(t *testing.T) {
	m := newMiddleware(t, filepath.Join(t.TempDir(), "absent.md"))
	if got := m.format(nil); !strings.Contains(got, "(No memory loaded)") {
		t.Fatalf("format = %q", got)
	}
}

func TestNewRejectsTemplateWithoutSlot(t *testing.T) {
	_, err := New(Options{Backend: backend.NewFilesystemBackend(backend.FilesystemOptions{}), SystemPrompt: "no slot"})
	if err == nil {
		t.Fatal("expected error")
	}
}
