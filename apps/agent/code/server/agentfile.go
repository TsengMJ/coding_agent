package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AgentTokenEnvVar fixes the `quill serve` token instead of a random one;
// clients send it too.
const AgentTokenEnvVar = "QUILL_AGENT_TOKEN"

// AgentFile is what a running `quill serve` publishes for local clients
// (the desktop app, `quill -server`).
type AgentFile struct {
	URL   string `json:"url"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
}

// WriteAgentFile replaces path atomically; only the user can read it
// because the token lets a client run commands.
func WriteAgentFile(path string, f AgentFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("agent file: %w", err)
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".agent-*.json")
	if err != nil {
		return fmt.Errorf("agent file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("agent file: %w", err)
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("agent file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("agent file: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("agent file: %w", err)
	}
	return nil
}

// ReadAgentFile reads the file WriteAgentFile wrote.
func ReadAgentFile(path string) (AgentFile, error) {
	var f AgentFile
	b, err := os.ReadFile(path)
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return f, fmt.Errorf("agent file %s: %w", path, err)
	}
	return f, nil
}

// RemoveAgentFile deletes path if the server with pid wrote it; a newer
// server may have replaced it.
func RemoveAgentFile(path string, pid int) {
	if f, err := ReadAgentFile(path); err == nil && f.PID == pid {
		_ = os.Remove(path)
	}
}

// ServeToken is $QUILL_AGENT_TOKEN or a random token.
func ServeToken() (string, error) {
	if tok := os.Getenv(AgentTokenEnvVar); tok != "" {
		return tok, nil
	}
	return NewToken()
}

// ClientToken is $QUILL_AGENT_TOKEN, else the token in the agent file when
// it was written by the server at serverURL, else "".
func ClientToken(agentFile, serverURL string) string {
	if tok := os.Getenv(AgentTokenEnvVar); tok != "" {
		return tok
	}
	f, err := ReadAgentFile(agentFile)
	if err != nil || strings.TrimRight(f.URL, "/") != strings.TrimRight(serverURL, "/") {
		return ""
	}
	return f.Token
}
