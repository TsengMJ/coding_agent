package client

import (
	"context"
	"strings"
	"testing"

	"github.com/tsengmingchun/deepagents_golang/code/askuser"
	"github.com/tsengmingchun/deepagents_golang/code/commands"
	"github.com/tsengmingchun/deepagents_golang/code/server"
)

type fakeSession struct {
	calls []string
	auto  bool
	info  []string
	model string
	mode  string
	usage *server.ThreadUsage
}

func (s *fakeSession) Thread() string                      { return "t1" }
func (s *fakeSession) NewThread()                          { s.calls = append(s.calls, "new") }
func (s *fakeSession) ListThreads(context.Context)         { s.calls = append(s.calls, "threads") }
func (s *fakeSession) Resume(_ context.Context, id string) { s.calls = append(s.calls, "resume "+id) }
func (s *fakeSession) Compact(context.Context)             { s.calls = append(s.calls, "compact") }
func (s *fakeSession) AutoApprove() bool                   { return s.auto }
func (s *fakeSession) SetAutoApprove(on bool)              { s.auto = on }
func (s *fakeSession) Quit()                               { s.calls = append(s.calls, "quit") }
func (s *fakeSession) Info(text string)                    { s.info = append(s.info, text) }
func (s *fakeSession) Mode() string                        { return s.mode }
func (s *fakeSession) SetMode(mode string)                 { s.mode = mode }
func (s *fakeSession) Build(context.Context)               { s.calls = append(s.calls, "build") }
func (s *fakeSession) Model() string                       { return s.model }
func (s *fakeSession) SetModel(spec string)                { s.model = spec }
func (s *fakeSession) ShowUsage(_ context.Context, format func(*server.ThreadUsage) string) {
	s.Info(format(s.usage))
}

func TestCommandRegistryCoversEveryDCodeCommand(t *testing.T) {
	s := &fakeSession{}
	reg := NewCommandRegistry(s, CommandOptions{HelpFooter: "keys: x"})
	for _, c := range commands.DCode {
		if _, _, ok := reg.Lookup(c.Name); !ok {
			t.Errorf("%s not registered", c.Name)
		}
	}
	ctx := context.Background()
	for _, line := range []string{"/clear", "/offload", "/resume abc", "/exit", "/yolo"} {
		if err := reg.Dispatch(ctx, line); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
	}
	if got := strings.Join(s.calls, ","); got != "new,compact,resume abc,quit" || !s.auto {
		t.Fatalf("calls = %q auto = %v", got, s.auto)
	}
	if err := reg.Dispatch(ctx, "/resume"); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("missing id: %v", err)
	}
	if err := reg.Dispatch(ctx, "/help"); err != nil || !strings.Contains(s.info[len(s.info)-1], "keys: x") {
		t.Fatalf("help = %v %q", err, s.info)
	}
}

func TestPlanTogglesModeAndBuildStarts(t *testing.T) {
	s := &fakeSession{}
	reg := NewCommandRegistry(s, CommandOptions{})
	ctx := context.Background()
	if err := reg.Dispatch(ctx, "/plan"); err != nil || s.mode != server.ModePlan {
		t.Fatalf("plan on: %v mode=%q", err, s.mode)
	}
	if err := reg.Dispatch(ctx, "/plan"); err != nil || s.mode != server.ModeAgent {
		t.Fatalf("plan off: %v mode=%q", err, s.mode)
	}
	if err := reg.Dispatch(ctx, "/build"); err != nil || strings.Join(s.calls, ",") != "build" {
		t.Fatalf("build: %v calls=%v", err, s.calls)
	}
}

func TestParseAnswerLine(t *testing.T) {
	q := askuser.Question{ID: "q", Options: []askuser.Option{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}, {ID: "c", Label: "C"}}}
	if a := ParseAnswerLine(q, "2"); len(a.OptionIDs) != 1 || a.OptionIDs[0] != "b" {
		t.Fatalf("single = %+v", a)
	}
	if a := ParseAnswerLine(q, "1,3"); len(a.OptionIDs) != 1 {
		t.Fatalf("single-choice question took several: %+v", a)
	}
	q.AllowMultiple = true
	if a := ParseAnswerLine(q, "1, 3"); strings.Join(a.OptionIDs, ",") != "a,c" {
		t.Fatalf("multi = %+v", a)
	}
	if a := ParseAnswerLine(q, "use redis"); a.Other != "use redis" || a.OptionIDs != nil {
		t.Fatalf("other = %+v", a)
	}
	if a := ParseAnswerLine(q, "9"); a.Other != "9" {
		t.Fatalf("out of range = %+v", a)
	}
}

func TestModelCommandShowsAndSwitches(t *testing.T) {
	s := &fakeSession{model: "openai:a"}
	reg := NewCommandRegistry(s, CommandOptions{})
	ctx := context.Background()
	if err := reg.Dispatch(ctx, "/model"); err != nil || !strings.HasPrefix(s.info[0], "model openai:a (providers: ") || !strings.Contains(s.info[0], "openai") {
		t.Fatalf("show: %v %q", err, s.info)
	}
	if err := reg.Dispatch(ctx, "/model openrouter:x/y"); err != nil || s.model != "openrouter:x/y" {
		t.Fatalf("switch: %v model=%q", err, s.model)
	}
}

func TestModelCommandRejectsBadInput(t *testing.T) {
	s := &fakeSession{model: "openai:a"}
	reg := NewCommandRegistry(s, CommandOptions{})
	for _, line := range []string{"/model nope:x", "/model a b", "/model --bogus", "/model openai:b --default"} {
		if err := reg.Dispatch(context.Background(), line); err == nil {
			t.Errorf("%s: want error", line)
		}
	}
}

func TestModelCommandSavesDefault(t *testing.T) {
	s := &fakeSession{model: "openai:a"}
	var saved []string
	reg := NewCommandRegistry(s, CommandOptions{SaveDefaultModel: func(spec string) error {
		saved = append(saved, spec)
		return nil
	}})
	ctx := context.Background()
	if err := reg.Dispatch(ctx, "/model openai:b --default"); err != nil {
		t.Fatal(err)
	}
	if err := reg.Dispatch(ctx, "/model --default"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(saved, ","); got != "openai:b,openai:b" || s.model != "openai:b" {
		t.Fatalf("saved = %q model = %q", got, s.model)
	}
}

func TestModelChoiceCurrent(t *testing.T) {
	cases := []struct {
		c    ModelChoice
		want string
	}{
		{ModelChoice{Override: "o", Thread: "t", Default: "d"}, "o"},
		{ModelChoice{Thread: "t", Default: "d"}, "t"},
		{ModelChoice{Default: "d"}, "d"},
		{ModelChoice{}, ""},
	}
	for _, tc := range cases {
		if got := tc.c.Current(); got != tc.want {
			t.Errorf("%+v: got %q want %q", tc.c, got, tc.want)
		}
	}
}
