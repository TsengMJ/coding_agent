package client

import (
	"context"
	"strings"
	"testing"

	"github.com/tsengmingchun/deepagents_golang/code/commands"
)

type fakeSession struct {
	calls []string
	auto  bool
	info  []string
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

func TestCommandRegistryCoversEveryDCodeCommand(t *testing.T) {
	s := &fakeSession{}
	reg := NewCommandRegistry(s, "keys: x")
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
