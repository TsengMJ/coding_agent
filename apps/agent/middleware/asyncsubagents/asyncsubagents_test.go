package asyncsubagents

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tsengmingchun/quill/apps/agent/agent"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/runtime"
)

// fakeServer is a minimal agent server: threads are "th-N", runs "run-N";
// a run reports success with the thread's last message "done: <input>".
type fakeServer struct {
	mu        sync.Mutex
	threads   int
	runs      map[string]map[string]any
	lastInput map[string]string
	cancelled []string
	headers   http.Header
	failStart bool
}

func newFakeServer(t *testing.T) (*fakeServer, *httptest.Server) {
	f := &fakeServer{runs: map[string]map[string]any{}, lastInput: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /threads", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.headers = r.Header.Clone()
		if f.failStart {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		f.threads++
		writeJSON(w, map[string]any{"thread_id": fmt.Sprintf("th-%d", f.threads)})
	})
	mux.HandleFunc("POST /threads/{thread}/runs", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			AssistantID string `json:"assistant_id"`
			Input       struct {
				Messages []struct {
					Content string `json:"content"`
				} `json:"messages"`
			} `json:"input"`
			Multitask string `json:"multitask_strategy"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.AssistantID != "graph" {
			http.Error(w, "bad run", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		id := fmt.Sprintf("run-%d", len(f.runs)+1)
		thread := r.PathValue("thread")
		f.runs[id] = map[string]any{"run_id": id, "thread_id": thread, "status": "success", "multitask": body.Multitask}
		f.lastInput[thread] = body.Input.Messages[0].Content
		writeJSON(w, f.runs[id])
	})
	mux.HandleFunc("GET /threads/{thread}/runs/{run}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		writeJSON(w, f.runs[r.PathValue("run")])
	})
	mux.HandleFunc("POST /threads/{thread}/runs/{run}/cancel", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.cancelled = append(f.cancelled, r.PathValue("run"))
		f.runs[r.PathValue("run")]["status"] = "interrupted"
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /threads/{thread}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		writeJSON(w, map[string]any{"values": map[string]any{"messages": []any{
			map[string]any{"type": "human", "content": f.lastInput[r.PathValue("thread")]},
			map[string]any{"type": "ai", "content": "done: " + f.lastInput[r.PathValue("thread")]},
		}}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return f, srv
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func call(id, name string, args map[string]any) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: name, Args: args}}}
}

func runAgent(t *testing.T, url string, replies ...llm.Message) (*runtime.State, *llm.FakeModel) {
	t.Helper()
	mw, err := New([]AsyncSubAgent{{Name: "researcher", Description: "Researches", GraphID: "graph", URL: url}},
		Options{SystemPrompt: "Use async agents.", Getenv: func(k string) string {
			if k == "LANGSMITH_API_KEY" {
				return "ls-key"
			}
			return ""
		}})
	if err != nil {
		t.Fatal(err)
	}
	model := llm.NewFakeModel(append(replies, llm.NewAssistantMessage("ok"))...)
	a, err := agent.CreateAgent(agent.Config{Model: model, Middleware: []agent.Middleware{mw}})
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, runtime.RunConfig{})
	if err != nil {
		t.Fatal(err)
	}
	return st, model
}

func toolText(st *runtime.State, id string) string {
	for _, m := range st.Messages {
		if m.Role == llm.RoleTool && m.ToolCallID == id {
			return m.Text()
		}
	}
	return "<missing>"
}

func TestAsyncTaskLifecycle(t *testing.T) {
	f, srv := newFakeServer(t)
	st, model := runAgent(t, srv.URL,
		call("s", ToolStart, map[string]any{"subagent_type": "researcher", "description": "find X"}),
		call("c", ToolCheck, map[string]any{"task_id": "th-1"}),
		call("u", ToolUpdate, map[string]any{"task_id": " th-1 ", "message": "also Y"}),
		call("x", ToolCancel, map[string]any{"task_id": "th-1"}),
		call("l", ToolList, map[string]any{}),
	)
	if got := toolText(st, "s"); got != "Launched async subagent. task_id: th-1" {
		t.Fatalf("start = %q", got)
	}
	var check map[string]any
	if err := json.Unmarshal([]byte(toolText(st, "c")), &check); err != nil || check["status"] != "success" || check["result"] != "done: find X" {
		t.Fatalf("check = %s (%v)", toolText(st, "c"), err)
	}
	if got := toolText(st, "u"); !strings.Contains(got, "Updated async subagent") || f.runs["run-2"]["multitask"] != "interrupt" || f.lastInput["th-1"] != "also Y" {
		t.Fatalf("update = %q, runs = %v", got, f.runs)
	}
	if len(f.cancelled) != 1 || f.cancelled[0] != "run-2" {
		t.Fatalf("cancelled = %v", f.cancelled)
	}
	if got := toolText(st, "l"); !strings.Contains(got, "1 tracked task(s)") || !strings.Contains(got, "status: cancelled") {
		t.Fatalf("list = %q", got)
	}
	task := TasksFrom(st.Values[TasksStateKey])["th-1"]
	if task.RunID != "run-2" || task.Status != "cancelled" || task.AgentName != "researcher" || task.CreatedAt == "" {
		t.Fatalf("tracked task = %+v", task)
	}
	if f.headers.Get("x-auth-scheme") != "langsmith" || f.headers.Get("x-api-key") != "ls-key" {
		t.Fatalf("headers = %v", f.headers)
	}
	req := model.Requests()[0]
	if !strings.Contains(req.Messages[0].Text(), "Available async subagent types:\n\n- researcher: Researches") {
		t.Fatalf("system prompt:\n%s", req.Messages[0].Text())
	}
	var names []string
	for _, s := range req.Tools {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "start_async_task,check_async_task,update_async_task,cancel_async_task,list_async_tasks" {
		t.Fatalf("tools = %v", names)
	}
}

func TestAsyncToolErrorsGoToTheModel(t *testing.T) {
	f, srv := newFakeServer(t)
	f.failStart = true
	st, _ := runAgent(t, srv.URL,
		call("a", ToolStart, map[string]any{"subagent_type": "nope", "description": "x"}),
		call("b", ToolStart, map[string]any{"subagent_type": "researcher", "description": "x"}),
		call("c", ToolCheck, map[string]any{"task_id": "missing"}),
		call("d", ToolList, map[string]any{"status_filter": "running"}),
	)
	for id, want := range map[string]string{
		"a": "Unknown async subagent type `nope`. Available types: `researcher`",
		"b": "Failed to launch async subagent 'researcher'",
		"c": `No tracked task found for task_id: "missing"`,
		"d": "No async subagent tasks tracked.",
	} {
		if got := toolText(st, id); !strings.Contains(got, want) {
			t.Errorf("%s = %q, want %q", id, got, want)
		}
	}
}

func TestNewValidatesAgents(t *testing.T) {
	ok := AsyncSubAgent{Name: "a", GraphID: "g", URL: "http://x"}
	cases := map[string][]AsyncSubAgent{
		"none":      nil,
		"no name":   {{GraphID: "g", URL: "http://x"}},
		"duplicate": {ok, ok},
		"no graph":  {{Name: "a", URL: "http://x"}},
		"no url":    {{Name: "a", GraphID: "g"}},
	}
	for name, agents := range cases {
		if _, err := New(agents, Options{}); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestTasksFromCheckpointJSON(t *testing.T) {
	in := map[string]Task{"t": {TaskID: "t", AgentName: "a", Status: "running"}}
	raw, _ := json.Marshal(in)
	var back any
	_ = json.Unmarshal(raw, &back)
	merged, err := mergeTasks(back, map[string]Task{"u": {TaskID: "u"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := merged.(map[string]Task); len(got) != 2 || got["t"].AgentName != "a" {
		t.Fatalf("merged = %+v", got)
	}
}
