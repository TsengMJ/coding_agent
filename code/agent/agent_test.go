package agent

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/tsengmingchun/deepagents_golang/llm"
)

func TestSystemPromptHeadless(t *testing.T) {
	got, err := SystemPrompt(Options{Cwd: "/work/proj", Model: llm.NewFakeModel()})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"non-interactive (headless) mode", "The project root is `/work/proj`", "You are running as model `fake` (provider: fake)"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if left := regexp.MustCompile(`\{[a-z_]+\}`).FindAllString(got, -1); len(left) > 0 {
		t.Fatalf("unreplaced placeholders: %v", left)
	}
}

func TestSystemPromptInteractive(t *testing.T) {
	got, _ := SystemPrompt(Options{Cwd: "/w", Interactive: true})
	if !strings.Contains(got, "an interactive TUI") {
		t.Fatal("interactive prompt missing mode description")
	}
}

func TestCreateCLIAgentRequiresCwd(t *testing.T) {
	if _, err := CreateCLIAgent(context.Background(), Options{Model: llm.NewFakeModel()}); err == nil {
		t.Fatal("CreateCLIAgent without Cwd must fail")
	}
}
