package profiles

import (
	"slices"
	"testing"

	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/llm"
)

func ptr[T any](v T) *T { return &v }

type namedMiddleware struct{ name, tag string }

func (n namedMiddleware) Name() string { return n.name }

func names(ms []agent.Middleware) []string {
	var out []string
	for _, m := range ms {
		n := m.(namedMiddleware)
		out = append(out, n.name+n.tag)
	}
	return out
}

func TestApplyPrompt(t *testing.T) {
	cases := []struct {
		name string
		p    HarnessProfile
		base string
		want string
	}{
		{"empty profile keeps base", HarnessProfile{}, "user", "user"},
		{"base replaced", HarnessProfile{BaseSystemPrompt: ptr("profile")}, "user", "profile"},
		{"suffix appended", HarnessProfile{SystemPromptSuffix: ptr("be brief")}, "user", "user\n\nbe brief"},
		{"suffix alone", HarnessProfile{SystemPromptSuffix: ptr("be brief")}, "", "be brief"},
		{"both", HarnessProfile{BaseSystemPrompt: ptr("b"), SystemPromptSuffix: ptr("s")}, "user", "b\n\ns"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ApplyPrompt(tc.p, tc.base); got != tc.want {
				t.Fatalf("ApplyPrompt = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMergeHarnessProfiles(t *testing.T) {
	base := HarnessProfile{
		SystemPromptSuffix:       ptr("provider suffix"),
		ToolDescriptionOverrides: map[string]string{"task": "base task", "ls": "base ls"},
		ExcludedTools:            []string{"execute"},
		ExtraMiddleware: func() []agent.Middleware {
			return []agent.Middleware{namedMiddleware{"A", "1"}, namedMiddleware{"B", "1"}}
		},
		GeneralPurposeSubagent: &GeneralPurposeSubagentProfile{Enabled: ptr(false), Description: ptr("base gp")},
	}
	override := HarnessProfile{
		BaseSystemPrompt:         ptr("model base"),
		ToolDescriptionOverrides: map[string]string{"task": "model task"},
		ExcludedTools:            []string{"grep", "execute"},
		ExtraMiddleware: func() []agent.Middleware {
			return []agent.Middleware{namedMiddleware{"A", "2"}, namedMiddleware{"C", "2"}}
		},
		GeneralPurposeSubagent: &GeneralPurposeSubagentProfile{Enabled: ptr(true)},
	}
	got := MergeHarnessProfiles(base, override)
	if *got.BaseSystemPrompt != "model base" || *got.SystemPromptSuffix != "provider suffix" {
		t.Fatalf("prompts = %v %v", *got.BaseSystemPrompt, *got.SystemPromptSuffix)
	}
	if got.ToolDescriptionOverrides["task"] != "model task" || got.ToolDescriptionOverrides["ls"] != "base ls" {
		t.Fatalf("tool overrides = %v", got.ToolDescriptionOverrides)
	}
	if !slices.Equal(got.ExcludedTools, []string{"execute", "grep"}) {
		t.Fatalf("excluded tools = %v", got.ExcludedTools)
	}
	if mw := names(got.Materialize()); !slices.Equal(mw, []string{"A2", "B1", "C2"}) {
		t.Fatalf("extra middleware = %v", mw)
	}
	gp := got.GeneralPurposeSubagent
	if !*gp.Enabled || *gp.Description != "base gp" {
		t.Fatalf("gp = %+v", gp)
	}
	if base.ToolDescriptionOverrides["task"] != "base task" {
		t.Fatal("merge must not modify base")
	}
}

func TestHarnessProfileForLayersExactOnProvider(t *testing.T) {
	t.Cleanup(resetHarnessProfiles)
	if err := RegisterHarnessProfile("acme", HarnessProfile{SystemPromptSuffix: ptr("acme"), ExcludedTools: []string{"execute"}}); err != nil {
		t.Fatal(err)
	}
	if err := RegisterHarnessProfile("acme:big", HarnessProfile{BaseSystemPrompt: ptr("big")}); err != nil {
		t.Fatal(err)
	}
	if err := RegisterHarnessProfile("acme:big", HarnessProfile{ExcludedTools: []string{"grep"}}); err != nil {
		t.Fatal(err)
	}

	p, err := HarnessProfileFor(nil, "acme:big")
	if err != nil {
		t.Fatal(err)
	}
	if ApplyPrompt(p, "u") != "big\n\nacme" || !slices.Equal(p.ExcludedTools, []string{"execute", "grep"}) {
		t.Fatalf("profile = %+v", p)
	}
	p, _ = HarnessProfileFor(nil, "acme:small")
	if ApplyPrompt(p, "u") != "u\n\nacme" {
		t.Fatalf("provider fallback = %+v", p)
	}
	p, _ = HarnessProfileFor(llm.NewFakeModel(), "")
	if p.SystemPromptSuffix != nil || len(p.ExcludedTools) > 0 {
		t.Fatalf("unmatched model = %+v", p)
	}
	if err := RegisterHarnessProfile("acme:", HarnessProfile{}); err == nil {
		t.Fatal("want error for malformed key")
	}
}

func TestHarnessProfileForUsesModelInfo(t *testing.T) {
	t.Cleanup(resetHarnessProfiles)
	info := llm.NewFakeModel().Info()
	if err := RegisterHarnessProfile(info.Provider+":"+info.Model, HarnessProfile{SystemPromptSuffix: ptr("fake")}); err != nil {
		t.Fatal(err)
	}
	p, err := HarnessProfileFor(llm.NewFakeModel(), "")
	if err != nil || ApplyPrompt(p, "") != "fake" {
		t.Fatalf("profile = %+v, %v", p, err)
	}
}
