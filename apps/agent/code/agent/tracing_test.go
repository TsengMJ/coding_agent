package agent

import (
	"testing"

	"github.com/tsengmingchun/quill/apps/agent/code/config"
	"github.com/tsengmingchun/quill/apps/agent/langsmith"
)

func clearLangSmithEnv(t *testing.T) {
	for _, names := range [][]string{langsmith.TracingEnvVars, langsmith.APIKeyEnvVars, langsmith.ProjectEnvVars, langsmith.EndpointEnvVars} {
		for _, k := range names {
			t.Setenv(k, "")
		}
	}
}

func TestNewLangSmithIsOffByDefault(t *testing.T) {
	clearLangSmithEnv(t)
	t.Setenv("LANGSMITH_API_KEY", "k")
	c, err := NewLangSmith(&config.Settings{})
	if err != nil || c != nil {
		t.Fatalf("NewLangSmith = %v, %v", c, err)
	}
}

func TestNewLangSmithFromConfig(t *testing.T) {
	clearLangSmithEnv(t)
	t.Setenv("LANGSMITH_API_KEY", "k")
	c, err := NewLangSmith(&config.Settings{LangSmithTracing: true})
	if err != nil || c == nil {
		t.Fatalf("NewLangSmith = %v, %v", c, err)
	}
	defer c.Close()
	if c.Project() != DefaultLangSmithProject {
		t.Fatalf("project = %q", c.Project())
	}
}

func TestNewLangSmithProjectPrecedence(t *testing.T) {
	clearLangSmithEnv(t)
	t.Setenv("LANGSMITH_API_KEY", "k")
	t.Setenv("LANGSMITH_TRACING", "true")
	t.Setenv("LANGSMITH_PROJECT", "from-env")
	c, err := NewLangSmith(nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Project() != "from-env" {
		t.Fatalf("project = %q", c.Project())
	}
	c.Close()
	c, err = NewLangSmith(&config.Settings{LangSmithProject: "from-config"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Project() != "from-config" {
		t.Fatalf("project = %q", c.Project())
	}
}

func TestNewLangSmithNeedsKey(t *testing.T) {
	clearLangSmithEnv(t)
	if _, err := NewLangSmith(&config.Settings{LangSmithTracing: true}); err == nil {
		t.Fatal("tracing on without a key must fail")
	}
}
