package main

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tsengmingchun/deepagents_golang/code/config"
	"github.com/tsengmingchun/deepagents_golang/code/server"
	"github.com/tsengmingchun/deepagents_golang/code/sessions"
	"github.com/tsengmingchun/deepagents_golang/code/storage"
	"github.com/tsengmingchun/deepagents_golang/llm"
)

func TestResolvePrompt(t *testing.T) {
	tests := []struct {
		name    string
		flag    string
		args    []string
		want    string
		wantErr bool
	}{
		{name: "flag", flag: "summarize README", want: "summarize README"},
		{name: "args", args: []string{"fix", "the", "tests"}, want: "fix the tests"},
		{name: "empty stays interactive"},
		{name: "both", flag: "a", args: []string{"b"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolvePrompt(tt.flag, tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("resolvePrompt: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInteractiveUI(t *testing.T) {
	tests := []struct {
		name                         string
		plain, useTUI, stdin, stdout bool
		want                         ui
	}{
		{name: "default is GUI", stdin: true, stdout: true, want: uiGUI},
		{name: "GUI without a stdout terminal", stdin: true, want: uiGUI},
		{name: "-tui", useTUI: true, stdin: true, stdout: true, want: uiTUI},
		{name: "-tui without stdout terminal falls back to REPL", useTUI: true, stdin: true, want: uiREPL},
		{name: "-plain", plain: true, stdin: true, stdout: true, want: uiREPL},
		{name: "piped stdin", stdout: true, want: uiREPL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := interactiveUI(tt.plain, tt.useTUI, tt.stdin, tt.stdout); got != tt.want {
				t.Fatalf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestGUIURLKeepsTokenInFragment(t *testing.T) {
	got := guiURL(&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4321}, "abc")
	if got != "http://127.0.0.1:4321/#token=abc" {
		t.Fatalf("guiURL = %q", got)
	}
}

func TestDefaultGUIThreadResumesSavedConversation(t *testing.T) {
	conn, redisURL := storage.PostgresConnFromEnv(), storage.RedisURLFromEnv()
	if conn == "" || redisURL == "" {
		t.Skip("Postgres or Redis is not configured")
	}
	ctx := context.Background()
	cwd := t.TempDir()
	thread := "gui-" + sessions.NewThreadID()
	pool, err := pgxpool.New(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		defer pool.Close()
		_, _ = pool.Exec(context.Background(), "DELETE FROM deepagents_checkpoints WHERE execution_id = $1", thread)
		_, _ = pool.Exec(context.Background(), "DELETE FROM dcode_threads WHERE thread_id = $1", thread)
	})
	open := func() *server.Service {
		st, err := storage.Open(ctx, conn, redisURL)
		if err != nil {
			t.Fatal(err)
		}
		svc, err := server.New(ctx, server.Config{
			Model:   llm.NewFakeModel(llm.NewAssistantMessage("done")),
			Storage: st,
		})
		if err != nil {
			t.Fatal(err)
		}
		return svc
	}
	first := open()
	if id, err := defaultGUIThread(ctx, first, cwd); err != nil || len(id) != 12 {
		t.Fatalf("new thread = %q, %v", id, err)
	}
	for _, err := range first.Run(ctx, server.RunRequest{
		ThreadID: thread, Cwd: cwd, Messages: []llm.Message{llm.NewUserMessage("hello")}, AutoApprove: true,
	}) {
		if err != nil {
			t.Fatal(err)
		}
	}
	_ = first.Close()
	second := open()
	defer second.Close()
	if id, err := defaultGUIThread(ctx, second, cwd); err != nil || id != thread {
		t.Fatalf("resumed thread = %q, %v", id, err)
	}
}

func TestStartupFailsWithoutRedis(t *testing.T) {
	t.Setenv("DCODE_POSTGRES_CONN_STRING", "postgres://x@127.0.0.1:1/x")
	t.Setenv("DCODE_REDIS_URL", "")
	t.Setenv("REDIS_URL", "")
	t.Setenv(config.ServerEnvVar, "")
	var stdout, stderr bytes.Buffer
	_, err := run(context.Background(), []string{"-n", "hi"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "DCODE_REDIS_URL") {
		t.Fatalf("err = %v", err)
	}
}

func TestStartupFailsWithoutPostgres(t *testing.T) {
	t.Setenv("DCODE_POSTGRES_CONN_STRING", "")
	t.Setenv("POSTGRES_CONN_STRING", "")
	t.Setenv("DCODE_REDIS_URL", "redis://127.0.0.1:1/0")
	t.Setenv(config.ServerEnvVar, "")
	var stdout, stderr bytes.Buffer
	_, err := run(context.Background(), []string{"-n", "hi"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "DCODE_POSTGRES_CONN_STRING") {
		t.Fatalf("err = %v", err)
	}
}

func TestHeadlessFlagsRequireAPrompt(t *testing.T) {
	for _, args := range [][]string{{"-q"}, {"-no-stream"}, {"-timeout", "5s"}} {
		var stdout, stderr bytes.Buffer
		code, err := run(context.Background(), args, &stdout, &stderr)
		if code != 2 || err == nil || !strings.Contains(err.Error(), "requires -n") {
			t.Errorf("%v: code=%d err=%v", args, code, err)
		}
	}
}

func TestNegativeTimeoutIsRejected(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code, err := run(context.Background(), []string{"-timeout", "-1s", "-n", "hi"}, &stdout, &stderr)
	if code != 2 || err == nil {
		t.Fatalf("code=%d err=%v", code, err)
	}
}
