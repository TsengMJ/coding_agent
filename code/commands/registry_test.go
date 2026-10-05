package commands

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func newTestRegistry(t *testing.T, got *string) *Registry {
	t.Helper()
	r := NewRegistry()
	record := func(name string) Handler {
		return func(_ context.Context, args string) error {
			*got = name + "|" + args
			return nil
		}
	}
	for _, c := range []Command{
		{Name: "/resume", Args: "<id>", Description: "switch thread"},
		{Name: "/quit", Aliases: []string{"/q", "/exit"}, Description: "exit"},
		{Name: "/threads", Description: "list threads"},
		{Name: "/debug", Hidden: true},
	} {
		if err := r.Register(c, record(c.Name)); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func TestDispatchResolvesAliasesAndArgs(t *testing.T) {
	var got string
	r := newTestRegistry(t, &got)
	if err := r.Dispatch(context.Background(), "  /q  "); err != nil || got != "/quit|" {
		t.Fatalf("alias dispatch: got %q, err %v", got, err)
	}
	if err := r.Dispatch(context.Background(), "/resume  abc 12 "); err != nil || got != "/resume|abc 12" {
		t.Fatalf("args dispatch: got %q, err %v", got, err)
	}
}

func TestDispatchRejectsNonCommands(t *testing.T) {
	var got string
	if err := newTestRegistry(t, &got).Dispatch(context.Background(), "hello"); !errors.Is(err, ErrNotCommand) {
		t.Fatalf("err = %v", err)
	}
}

func TestUnknownCommandSuggestsClosest(t *testing.T) {
	var got string
	r := newTestRegistry(t, &got)
	for input, want := range map[string]string{"/resum": "/resume", "/thraeds": "/threads", "/zzzzzz": ""} {
		err := r.Dispatch(context.Background(), input)
		var unk *UnknownCommandError
		if !errors.As(err, &unk) || unk.Suggestion != want {
			t.Errorf("%s: err = %v", input, err)
		}
	}
	if msg := r.Dispatch(context.Background(), "/resum").Error(); msg != "unknown command /resum — did you mean /resume? (try /help)" {
		t.Errorf("message = %q", msg)
	}
	if s := r.Suggest("/debu"); s == "/debug" {
		t.Error("hidden commands must not be suggested")
	}
}

func TestHelpHidesHiddenCommands(t *testing.T) {
	var got string
	help := newTestRegistry(t, &got).Help()
	if strings.Contains(help, "/debug") {
		t.Fatalf("help lists hidden command:\n%s", help)
	}
	for _, want := range []string{"/resume <id>  switch thread", "/quit", "(/q, /exit)"} {
		if !strings.Contains(help, want) {
			t.Errorf("help missing %q:\n%s", want, help)
		}
	}
}

func TestRegisterRejectsDuplicatesAndBadNames(t *testing.T) {
	r := NewRegistry()
	noop := func(context.Context, string) error { return nil }
	if err := r.Register(Command{Name: "/a", Aliases: []string{"/b"}}, noop); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(Command{Name: "/c", Aliases: []string{"/b"}}, noop); err == nil {
		t.Fatal("duplicate alias accepted")
	}
	if _, _, ok := r.Lookup("/c"); ok {
		t.Fatal("failed registration must not be partially applied")
	}
	if err := r.Register(Command{Name: "nope"}, noop); err == nil {
		t.Fatal("name without / accepted")
	}
}

func TestQuillCommandsAreUnique(t *testing.T) {
	r := NewRegistry()
	for _, c := range Quill {
		if err := r.Register(c, func(context.Context, string) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
}
