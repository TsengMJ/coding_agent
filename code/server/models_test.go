package server

import (
	"context"
	"errors"
	"testing"

	"github.com/tsengmingchun/deepagents_golang/llm"
)

func lastReply(t *testing.T, events []Event) llm.Message {
	t.Helper()
	msgs := messages(events)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleAssistant {
			return msgs[i]
		}
	}
	t.Fatal("no assistant message")
	return llm.Message{}
}

func TestServiceRunsOnRequestedModelAndThreadRemembersIt(t *testing.T) {
	svc, def := newService(t, llm.NewAssistantMessage("from default"))
	alt := llm.NewFakeModel(llm.NewAssistantMessage("alt 1"), llm.NewAssistantMessage("alt 2"))
	svc.newModel = func(spec string) (llm.ChatModel, error) {
		if spec != "server-test:alt" {
			t.Fatalf("built %q", spec)
		}
		return alt, nil
	}
	cwd := t.TempDir()

	req := userReq("t1", cwd, "hi")
	req.Model = "server-test:alt"
	events, err := collect(t, svc, req)
	if err != nil {
		t.Fatal(err)
	}
	if got := lastReply(t, events); got.Text() != "alt 1" || got.Metadata[ModelNameKey] != "server-test:alt" {
		t.Fatalf("reply = %q meta = %v", got.Text(), got.Metadata)
	}

	events, err = collect(t, svc, userReq("t1", cwd, "again"))
	if err != nil {
		t.Fatal(err)
	}
	if got := lastReply(t, events); got.Text() != "alt 2" {
		t.Fatalf("thread did not keep its model: %q", got.Text())
	}
	ts, err := svc.GetThread(context.Background(), "t1")
	if err != nil || ts.Model != "server-test:alt" {
		t.Fatalf("thread model = %+v, %v", ts, err)
	}
	if n := len(def.Requests()); n != 0 {
		t.Fatalf("default model called %d times", n)
	}

	events, err = collect(t, svc, userReq("t2", cwd, "new thread"))
	if err != nil {
		t.Fatal(err)
	}
	if got := lastReply(t, events); got.Text() != "from default" || got.Metadata[ModelNameKey] != "fake:fake" {
		t.Fatalf("new thread reply = %q meta = %v", got.Text(), got.Metadata)
	}
}

func TestServiceRejectsUnknownModel(t *testing.T) {
	svc, _ := newService(t)
	req := userReq("t1", t.TempDir(), "hi")
	req.Model = "nope:x"
	if _, err := collect(t, svc, req); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("err = %v, want ErrBadRequest", err)
	}
}

func TestServiceDefaultModelSpec(t *testing.T) {
	svc, _ := newService(t)
	if got := svc.DefaultModel(); got != "fake:fake" {
		t.Fatalf("DefaultModel = %q", got)
	}
	ts, err := svc.GetThread(context.Background(), "missing")
	if err != nil || ts != nil {
		t.Fatalf("missing thread = %+v, %v", ts, err)
	}
}
