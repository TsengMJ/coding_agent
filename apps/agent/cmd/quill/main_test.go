package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	codeagent "github.com/tsengmingchun/quill/apps/agent/code/agent"
	"github.com/tsengmingchun/quill/apps/agent/code/config"
	"github.com/tsengmingchun/quill/apps/agent/code/server"
	"github.com/tsengmingchun/quill/apps/agent/code/sessions"
	"github.com/tsengmingchun/quill/apps/agent/code/storage"
	"github.com/tsengmingchun/quill/apps/agent/llm"
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
		{name: "empty"},
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

func TestDefaultThreadResumesSavedConversation(t *testing.T) {
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
		_, _ = pool.Exec(context.Background(), "DELETE FROM quill_checkpoints WHERE execution_id = $1", thread)
		_, _ = pool.Exec(context.Background(), "DELETE FROM quill_threads WHERE thread_id = $1", thread)
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
	if id, err := server.DefaultThread(ctx, first, cwd); err != nil || len(id) != 12 {
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
	if id, err := server.DefaultThread(ctx, second, cwd); err != nil || id != thread {
		t.Fatalf("resumed thread = %q, %v", id, err)
	}
}

func TestStartupFailsWithoutRedis(t *testing.T) {
	t.Setenv("QUILL_POSTGRES_CONN_STRING", "postgres://x@127.0.0.1:1/x")
	t.Setenv("QUILL_REDIS_URL", "")
	t.Setenv("REDIS_URL", "")
	t.Setenv(config.ServerEnvVar, "")
	var stdout, stderr bytes.Buffer
	_, err := run(context.Background(), []string{"-n", "hi"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "QUILL_REDIS_URL") {
		t.Fatalf("err = %v", err)
	}
}

func TestStartupFailsWithoutPostgres(t *testing.T) {
	t.Setenv("QUILL_POSTGRES_CONN_STRING", "")
	t.Setenv("POSTGRES_CONN_STRING", "")
	t.Setenv("QUILL_REDIS_URL", "redis://127.0.0.1:1/0")
	t.Setenv(config.ServerEnvVar, "")
	var stdout, stderr bytes.Buffer
	_, err := run(context.Background(), []string{"-n", "hi"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "QUILL_POSTGRES_CONN_STRING") {
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

func TestDisableFlagRejectsUnknownFeatures(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code, err := run(context.Background(), []string{"-disable", "bogus", "-n", "hi"}, &stdout, &stderr)
	if code != 2 || err == nil || !strings.Contains(err.Error(), "unknown feature") {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestDisableDefaultsToTheShippedRepairLoop(t *testing.T) {
	t.Setenv(disableEnvVar, "")
	os.Unsetenv(disableEnvVar)
	if got, want := defaultDisable(), strings.Join(codeagent.DefaultDisabled, ","); got != want {
		t.Errorf("unset: %q, want %q", got, want)
	}
	t.Setenv(disableEnvVar, "")
	if got := defaultDisable(); got != "" {
		t.Errorf("empty env turns everything on, got %q", got)
	}
}

func TestNegativeTimeoutIsRejected(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code, err := run(context.Background(), []string{"-timeout", "-1s", "-n", "hi"}, &stdout, &stderr)
	if code != 2 || err == nil {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestNoPromptPointsToTheDesktopApp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code, err := run(context.Background(), nil, &stdout, &stderr)
	if code != 2 || err == nil || !strings.Contains(err.Error(), "quill serve") {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestConnectMCPWithoutConfigIsNil(t *testing.T) {
	s, err := connectMCP(context.Background(), filepath.Join(t.TempDir(), "mcp.json"))
	if err != nil || s != nil {
		t.Fatalf("session = %v, err = %v", s, err)
	}
}

func TestConnectMCPRejectsABadConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers": {"x": {}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := connectMCP(context.Background(), path); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v", err)
	}
}
