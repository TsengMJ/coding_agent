package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAgentFileRoundTripIsPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quill", "agent.json")
	want := AgentFile{URL: "http://127.0.0.1:8123", Token: "tok", PID: 42}
	if err := WriteAgentFile(path, want); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", st.Mode().Perm())
	}
	got, err := ReadAgentFile(path)
	if err != nil || got != want {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestRemoveAgentFileKeepsAnotherServersFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := WriteAgentFile(path, AgentFile{URL: "http://127.0.0.1:1", Token: "t", PID: 7}); err != nil {
		t.Fatal(err)
	}
	RemoveAgentFile(path, 8)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file of pid 7 removed by pid 8: %v", err)
	}
	RemoveAgentFile(path, 7)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file still there: %v", err)
	}
}

func TestServeTokenPrefersEnv(t *testing.T) {
	t.Setenv(AgentTokenEnvVar, "fixed")
	if tok, err := ServeToken(); err != nil || tok != "fixed" {
		t.Fatalf("got %q, %v", tok, err)
	}
	t.Setenv(AgentTokenEnvVar, "")
	tok, err := ServeToken()
	if err != nil || len(tok) < 32 {
		t.Fatalf("generated %q, %v", tok, err)
	}
}

func TestClientTokenReadsTheMatchingAgentFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := WriteAgentFile(path, AgentFile{URL: "http://127.0.0.1:8123", Token: "from-file", PID: 1}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(AgentTokenEnvVar, "")
	if got := ClientToken(path, "http://127.0.0.1:8123/"); got != "from-file" {
		t.Fatalf("matching URL: %q", got)
	}
	if got := ClientToken(path, "http://127.0.0.1:9999"); got != "" {
		t.Fatalf("other server got the token: %q", got)
	}
	t.Setenv(AgentTokenEnvVar, "from-env")
	if got := ClientToken(path, "http://127.0.0.1:9999"); got != "from-env" {
		t.Fatalf("env: %q", got)
	}
}
