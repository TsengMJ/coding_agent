package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tsengmingchun/deepagents_golang/code/config"
	"github.com/tsengmingchun/deepagents_golang/code/sessions"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/middleware/hitl"
	"github.com/tsengmingchun/deepagents_golang/runtime"
)

func init() {
	err := llm.RegisterProvider("server-test", func(string, map[string]any) (llm.ChatModel, error) {
		return llm.NewFakeModel(llm.NewAssistantMessage("pong")), nil
	})
	if err != nil {
		panic(err)
	}
}

func newService(t *testing.T, replies ...llm.Message) (*Service, *llm.FakeModel) {
	t.Helper()
	model := llm.NewFakeModel(replies...)
	svc, err := New(context.Background(), Config{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc, model
}

func collect(t *testing.T, srv Server, req RunRequest) ([]Event, error) {
	t.Helper()
	var events []Event
	for ev, err := range srv.Run(context.Background(), req) {
		if err != nil {
			return events, err
		}
		events = append(events, ev)
	}
	return events, nil
}

func userReq(thread, cwd, text string) RunRequest {
	return RunRequest{ThreadID: thread, Cwd: cwd, Messages: []llm.Message{llm.NewUserMessage(text)}, AutoApprove: true}
}

func messages(events []Event) []llm.Message {
	var out []llm.Message
	for _, ev := range events {
		if ev.Kind == EventMessage {
			out = append(out, *ev.Message)
		}
	}
	return out
}

func TestServiceRunWritesFilesAndRunsCommandsInCwd(t *testing.T) {
	cwd := t.TempDir()
	svc, _ := newService(t,
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "w", Name: "write_file",
			Args: map[string]any{"file_path": "/hello/main.go", "content": "package main\n"}}}},
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "x", Name: "execute",
			Args: map[string]any{"command": "cat hello/main.go"}}}},
		llm.NewAssistantMessage("created"),
	)
	events, err := collect(t, svc, userReq("t1", cwd, "make it"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(cwd, "hello", "main.go"))
	if err != nil || string(got) != "package main\n" {
		t.Fatalf("file on disk = %q, %v", got, err)
	}
	var execOut, final string
	for _, m := range messages(events) {
		if m.ToolCallID == "x" {
			execOut = m.Text()
		}
		if m.Role == llm.RoleAssistant {
			final = m.Text()
		}
	}
	if !strings.HasPrefix(execOut, "package main\n") {
		t.Fatalf("execute output = %q", execOut)
	}
	if final != "created" {
		t.Fatalf("final text = %q", final)
	}
}

func TestServiceContinuesThreadAndYieldsOnlyNewMessages(t *testing.T) {
	cwd := t.TempDir()
	svc, model := newService(t, llm.NewAssistantMessage("one"), llm.NewAssistantMessage("two"))
	first, err := collect(t, svc, userReq("t1", cwd, "first"))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(messages(first)); n != 2 {
		t.Fatalf("first run messages = %d, want 2 (user + assistant)", n)
	}
	second, err := collect(t, svc, userReq("t1", cwd, "second"))
	if err != nil {
		t.Fatal(err)
	}
	msgs := messages(second)
	if len(msgs) != 2 || msgs[0].Text() != "second" || msgs[1].Text() != "two" {
		t.Fatalf("second run messages = %+v", msgs)
	}
	reqs := model.Requests()
	var sawFirst bool
	for _, m := range reqs[1].Messages {
		if m.Text() == "one" {
			sawFirst = true
		}
	}
	if !sawFirst {
		t.Fatal("second model call did not include the first run's reply")
	}
	ts, err := svc.GetThread(context.Background(), "t1")
	if err != nil || ts == nil || len(ts.Messages) != 4 {
		t.Fatalf("GetThread = %+v, %v", ts, err)
	}
}

func writeReply(id, path, content string) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: "write_file",
		Args: map[string]any{"file_path": path, "content": content}}}}
}

func resumeReq(thread, cwd string, d ...hitl.Decision) RunRequest {
	return RunRequest{ThreadID: thread, Cwd: cwd, Resume: &Resume{Decisions: d}}
}

func TestServicePausesForApprovalAndResumes(t *testing.T) {
	cwd := t.TempDir()
	svc, model := newService(t, writeReply("w", "/a.txt", "A"), llm.NewAssistantMessage("written"))
	req := userReq("t1", cwd, "write a")
	req.AutoApprove = false
	events, err := collect(t, svc, req)
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.Kind != EventInterrupt || len(last.Interrupts) != 1 {
		t.Fatalf("last event = %+v", last)
	}
	ar := last.Interrupts[0].ActionRequests
	if len(ar) != 1 || ar[0].Name != "write_file" || ar[0].Args["file_path"] != "/a.txt" {
		t.Fatalf("action requests = %+v", ar)
	}
	if _, err := os.Stat(filepath.Join(cwd, "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("file written before approval: %v", err)
	}
	ts, _ := svc.GetThread(context.Background(), "t1")
	if ts == nil || len(ts.Interrupts) != 1 || ts.Interrupts[0].ActionRequests[0].ToolCallID != "w" {
		t.Fatalf("thread state = %+v", ts)
	}
	if _, err := collect(t, svc, userReq("t1", cwd, "something else")); !errors.Is(err, ErrPendingApproval) {
		t.Fatalf("want ErrPendingApproval, got %v", err)
	}

	events, err = collect(t, svc, resumeReq("t1", cwd, hitl.Decision{Type: hitl.DecisionApprove}))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(cwd, "a.txt")); string(got) != "A" {
		t.Fatalf("a.txt = %q", got)
	}
	msgs := messages(events)
	if len(msgs) != 2 || msgs[0].ToolCallID != "w" || msgs[1].Text() != "written" {
		t.Fatalf("resume messages = %+v", msgs)
	}
	if n := len(model.Requests()); n != 2 {
		t.Fatalf("model calls = %d, want 2", n)
	}
	if _, err := collect(t, svc, resumeReq("t1", cwd, hitl.Decision{Type: hitl.DecisionApprove})); !errors.Is(err, ErrNoPendingApproval) {
		t.Fatalf("want ErrNoPendingApproval, got %v", err)
	}
}

func TestServiceRejectKeepsFileUnwritten(t *testing.T) {
	cwd := t.TempDir()
	svc, _ := newService(t, writeReply("w", "/a.txt", "A"), llm.NewAssistantMessage("understood"))
	req := userReq("t1", cwd, "write a")
	req.AutoApprove = false
	if _, err := collect(t, svc, req); err != nil {
		t.Fatal(err)
	}
	events, err := collect(t, svc, resumeReq("t1", cwd, hitl.Decision{Type: hitl.DecisionReject, Message: "not now"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cwd, "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("rejected write happened: %v", err)
	}
	msgs := messages(events)
	if len(msgs) != 2 || msgs[0].Status != llm.ToolStatusError || msgs[0].Text() != "not now" || msgs[1].Text() != "understood" {
		t.Fatalf("messages = %+v", msgs)
	}
}

func TestServiceStreamsDeltasBeforeTheMessage(t *testing.T) {
	svc, _ := newService(t, llm.NewAssistantMessage("hello there"))
	events, err := collect(t, svc, userReq("t1", t.TempDir(), "hi"))
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	var text strings.Builder
	for _, ev := range events {
		kinds = append(kinds, string(ev.Kind))
		text.WriteString(ev.Text)
	}
	if got := strings.Join(kinds, ","); got != "message,delta,delta,message" {
		t.Fatalf("event kinds = %s", got)
	}
	if text.String() != "hello there" {
		t.Fatalf("streamed = %q", text.String())
	}
}

func TestConvertTracksEachNamespaceSeparately(t *testing.T) {
	values := func(ns []string, texts ...string) runtime.Event {
		st := &runtime.State{}
		for _, text := range texts {
			st.Messages = append(st.Messages, llm.NewAssistantMessage(text))
		}
		return runtime.Event{Mode: runtime.StreamValues, Namespace: ns, Data: st}
	}
	a, b := []string{"task:a"}, []string{"task:b"}
	sent := map[string]int{"": 1}
	var got []string
	for _, ev := range []runtime.Event{
		values(nil, "old", "m1"),
		values(a, "a1"),
		values(b, "b1"),
		values(a, "a1", "a2"),
		values(nil, "old", "m1", "m2"),
		values(b, "b1"),
		values(a, "a1", "a2", "a3"),
	} {
		for _, e := range convert(ev, sent) {
			got = append(got, strings.Join(e.Namespace, "/")+":"+e.Message.Text())
		}
	}
	want := ":m1 task:a:a1 task:b:b1 task:a:a2 :m2 task:a:a3"
	if s := strings.Join(got, " "); s != want {
		t.Fatalf("events = %q, want %q", s, want)
	}
}

func TestServiceRecordsSessions(t *testing.T) {
	cwd, other := t.TempDir(), t.TempDir()
	svc, _ := newService(t, llm.NewAssistantMessage("a"), llm.NewAssistantMessage("b"))
	if _, err := collect(t, svc, userReq("t1", cwd, "fix the bug\nmore detail")); err != nil {
		t.Fatal(err)
	}
	if _, err := collect(t, svc, userReq("t2", other, "elsewhere")); err != nil {
		t.Fatal(err)
	}
	list, err := svc.ListThreads(context.Background(), sessions.Filter{Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ThreadID != "t1" || list[0].Title != "fix the bug" || list[0].MessageCount != 2 {
		t.Fatalf("list = %+v", list)
	}
	all, _ := svc.ListThreads(context.Background(), sessions.Filter{})
	if len(all) != 2 || all[0].ThreadID != "t2" {
		t.Fatalf("all = %+v", all)
	}
}

func TestServiceRejectsCwdMismatch(t *testing.T) {
	svc, _ := newService(t, llm.NewAssistantMessage("a"), llm.NewAssistantMessage("b"))
	if _, err := collect(t, svc, userReq("t1", t.TempDir(), "x")); err != nil {
		t.Fatal(err)
	}
	_, err := collect(t, svc, userReq("t1", t.TempDir(), "y"))
	if !errors.Is(err, ErrThreadCwdMismatch) {
		t.Fatalf("want ErrThreadCwdMismatch, got %v", err)
	}
}

func TestServiceRejectsBusyThread(t *testing.T) {
	svc, _ := newService(t)
	if !svc.acquire("t1") {
		t.Fatal("first acquire failed")
	}
	defer svc.release("t1")
	_, err := collect(t, svc, userReq("t1", t.TempDir(), "x"))
	if !errors.Is(err, ErrThreadBusy) {
		t.Fatalf("want ErrThreadBusy, got %v", err)
	}
}

func TestServiceValidatesRequest(t *testing.T) {
	svc, _ := newService(t)
	cases := map[string]RunRequest{
		"relative cwd": userReq("t1", "rel/dir", "x"),
		"missing cwd":  userReq("t1", filepath.Join(t.TempDir(), "nope"), "x"),
		"no thread":    userReq("", t.TempDir(), "x"),
		"no input":     {ThreadID: "t1", Cwd: t.TempDir()},
	}
	for name, req := range cases {
		if _, err := collect(t, svc, req); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestServiceKeepsCwdsApart(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	write := func(id, content string) llm.Message {
		return llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: "write_file",
			Args: map[string]any{"file_path": "/f.txt", "content": content}}}}
	}
	svc, _ := newService(t, write("1", "A"), llm.NewAssistantMessage("ok"), write("2", "B"), llm.NewAssistantMessage("ok"))
	if _, err := collect(t, svc, userReq("ta", a, "x")); err != nil {
		t.Fatal(err)
	}
	if _, err := collect(t, svc, userReq("tb", b, "y")); err != nil {
		t.Fatal(err)
	}
	for dir, want := range map[string]string{a: "A", b: "B"} {
		got, err := os.ReadFile(filepath.Join(dir, "f.txt"))
		if err != nil || string(got) != want {
			t.Fatalf("%s/f.txt = %q, %v", dir, got, err)
		}
	}
}

func TestServicePostgresSurvivesRestart(t *testing.T) {
	conn := os.Getenv("POSTGRES_CONN_STRING")
	if conn == "" {
		t.Skip("POSTGRES_CONN_STRING is not set")
	}
	ctx := context.Background()
	cwd := t.TempDir()
	thread := "pg-" + sessions.NewThreadID()
	open := func(replies ...llm.Message) *Service {
		svc, err := New(ctx, Config{Model: llm.NewFakeModel(replies...), PostgresConn: conn})
		if err != nil {
			t.Fatal(err)
		}
		return svc
	}
	first := open(llm.NewAssistantMessage("one"))
	if first.Storage() != "postgres" {
		t.Fatalf("storage = %q", first.Storage())
	}
	if _, err := collect(t, first, userReq(thread, cwd, "hello")); err != nil {
		t.Fatal(err)
	}
	_ = first.Close()

	second := open(llm.NewAssistantMessage("two"))
	defer second.Close()
	t.Cleanup(func() {
		_ = second.checkpointer.DeleteThread(context.Background(), thread)
		_ = second.sessions.Delete(context.Background(), thread)
	})
	if _, err := collect(t, second, userReq(thread, t.TempDir(), "x")); !errors.Is(err, ErrThreadCwdMismatch) {
		t.Fatalf("cwd binding lost after restart: %v", err)
	}
	if _, err := collect(t, second, userReq(thread, cwd, "again")); err != nil {
		t.Fatal(err)
	}
	ts, err := second.GetThread(ctx, thread)
	if err != nil || ts == nil || len(ts.Messages) != 4 {
		t.Fatalf("thread after restart = %+v, %v", ts, err)
	}
	info, err := second.sessions.Get(ctx, thread)
	if err != nil || info == nil || info.Title != "hello" || info.MessageCount != 4 {
		t.Fatalf("session = %+v, %v", info, err)
	}
}

func TestServicePostgresResumesApprovalAfterRestart(t *testing.T) {
	conn := os.Getenv("POSTGRES_CONN_STRING")
	if conn == "" {
		t.Skip("POSTGRES_CONN_STRING is not set")
	}
	ctx := context.Background()
	cwd := t.TempDir()
	thread := "pg-" + sessions.NewThreadID()
	first, err := New(ctx, Config{Model: llm.NewFakeModel(writeReply("w", "/a.txt", "A")), PostgresConn: conn})
	if err != nil {
		t.Fatal(err)
	}
	req := userReq(thread, cwd, "write")
	req.AutoApprove = false
	events, err := collect(t, first, req)
	if err != nil || events[len(events)-1].Kind != EventInterrupt {
		t.Fatalf("events = %+v, %v", events, err)
	}
	_ = first.Close()

	second, err := New(ctx, Config{Model: llm.NewFakeModel(llm.NewAssistantMessage("done")), PostgresConn: conn})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	t.Cleanup(func() {
		_ = second.checkpointer.DeleteThread(context.Background(), thread)
		_ = second.sessions.Delete(context.Background(), thread)
	})
	ts, err := second.GetThread(ctx, thread)
	if err != nil || ts == nil || len(ts.Interrupts) != 1 || ts.Interrupts[0].ActionRequests[0].Name != "write_file" {
		t.Fatalf("pending approval lost after restart: %+v, %v", ts, err)
	}
	if _, err := collect(t, second, resumeReq(thread, cwd, hitl.Decision{Type: hitl.DecisionApprove})); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(cwd, "a.txt")); string(got) != "A" {
		t.Fatalf("a.txt = %q", got)
	}
}

func TestServeRequiresLoopback(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:0", "localhost:8123", "[::1]:0"} {
		if err := requireLoopback(addr); err != nil {
			t.Errorf("%s: %v", addr, err)
		}
	}
	for _, addr := range []string{":8123", "0.0.0.0:8123", "192.168.1.2:8123"} {
		if err := requireLoopback(addr); err == nil {
			t.Errorf("%s: want error", addr)
		}
	}
}

func TestNewUsesModelSpec(t *testing.T) {
	svc, err := New(context.Background(), Config{ModelSpec: "server-test:m"})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if svc.Storage() != "memory" {
		t.Fatalf("storage = %q", svc.Storage())
	}
}

func TestResolveModelSpecPrecedence(t *testing.T) {
	t.Setenv(ModelEnvVar, "env:model")
	if got := resolveModelSpec(Config{ModelSpec: "flag:model"}); got != "flag:model" {
		t.Errorf("flag should win, got %q", got)
	}
	if got := resolveModelSpec(Config{}); got != "env:model" {
		t.Errorf("env should win over default, got %q", got)
	}
	fromFile := Config{Settings: &config.Settings{Model: "file:model"}}
	if got := resolveModelSpec(fromFile); got != "env:model" {
		t.Errorf("env should win over config.toml, got %q", got)
	}
	t.Setenv(ModelEnvVar, "")
	if got := resolveModelSpec(fromFile); got != "file:model" {
		t.Errorf("config.toml should win over default, got %q", got)
	}
	if got := resolveModelSpec(Config{}); got != DefaultModelSpec {
		t.Errorf("want default, got %q", got)
	}
}

func TestModelOptionsCarryMaxRetries(t *testing.T) {
	if opts := modelOptions(Config{}); opts != nil {
		t.Errorf("no settings: opts = %v", opts)
	}
	zero := 0
	opts := modelOptions(Config{Settings: &config.Settings{MaxRetries: &zero}})
	if opts["max_retries"] != 0 {
		t.Errorf("opts = %v", opts)
	}
}

func TestServiceCompactSummarizesOlderMessagesForLaterRuns(t *testing.T) {
	cwd := t.TempDir()
	svc, model := newService(t,
		llm.NewAssistantMessage("a1"), llm.NewAssistantMessage("a2"),
		llm.NewAssistantMessage("a3"), llm.NewAssistantMessage("a4"),
		llm.NewAssistantMessage("SUMMARY-OF-EARLY-WORK"),
		llm.NewAssistantMessage("a5"),
	)
	for _, text := range []string{"first task", "q2", "q3", "q4"} {
		if _, err := collect(t, svc, userReq("t1", cwd, text)); err != nil {
			t.Fatal(err)
		}
	}
	res, err := svc.Compact(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Summary, "SUMMARY-OF-EARLY-WORK") {
		t.Fatalf("summary = %q", res.Summary)
	}
	if !strings.HasPrefix(res.FilePath, "/.deepagents/conversation_history/") {
		t.Fatalf("file path = %q", res.FilePath)
	}
	history, err := os.ReadFile(filepath.Join(cwd, filepath.FromSlash(res.FilePath)))
	if err != nil || !strings.Contains(string(history), "first task") {
		t.Fatalf("history = %q, %v", history, err)
	}

	if _, err := collect(t, svc, userReq("t1", cwd, "q5")); err != nil {
		t.Fatal(err)
	}
	reqs := model.Requests()
	var sent []string
	for _, m := range reqs[len(reqs)-1].Messages {
		sent = append(sent, m.Text())
	}
	joined := strings.Join(sent, "\n")
	if strings.Contains(joined, "first task") || !strings.Contains(joined, "SUMMARY-OF-EARLY-WORK") {
		t.Fatalf("model saw:\n%s", joined)
	}
	ts, err := svc.GetThread(context.Background(), "t1")
	if err != nil || len(ts.Messages) != 10 {
		t.Fatalf("stored messages = %d, %v; compaction must not drop history", len(ts.Messages), err)
	}
}

func TestServiceCompactShortThreadHasNothingToDo(t *testing.T) {
	cwd := t.TempDir()
	svc, _ := newService(t, llm.NewAssistantMessage("hi"))
	if _, err := collect(t, svc, userReq("t1", cwd, "hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Compact(context.Background(), "t1"); !errors.Is(err, ErrNothingToCompact) {
		t.Fatalf("err = %v, want ErrNothingToCompact", err)
	}
}

func TestServiceCompactUnknownThread(t *testing.T) {
	svc, _ := newService(t)
	if _, err := svc.Compact(context.Background(), "missing"); !errors.Is(err, ErrThreadNotFound) {
		t.Fatalf("err = %v, want ErrThreadNotFound", err)
	}
}
