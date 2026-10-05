package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tsengmingchun/quill/code/config"
	"github.com/tsengmingchun/quill/code/sessions"
	"github.com/tsengmingchun/quill/code/storage"
	"github.com/tsengmingchun/quill/llm"
)

func TestRenameThreadHTTP(t *testing.T) {
	svc, _ := newService(t, llm.NewAssistantMessage("done"))
	cwd := t.TempDir()
	if _, err := collect(t, svc, userReq("t1", cwd, "first")); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(svc)
	patch := func(id, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPatch, "/threads/"+id, strings.NewReader(body))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := patch("t1", `{"title":"  Investigate parser  "}`)
	var info sessions.Info
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &info) != nil || info.Title != "Investigate parser" {
		t.Fatalf("rename = %d %s", w.Code, w.Body.String())
	}
	if w := patch("t1", `{"title":"  "}`); w.Code != http.StatusBadRequest {
		t.Fatalf("empty title = %d", w.Code)
	}
	if w := patch("missing", `{"title":"Name"}`); w.Code != http.StatusNotFound {
		t.Fatalf("missing thread = %d", w.Code)
	}
}

func TestUnconfiguredServiceDoesNotPersistThreads(t *testing.T) {
	ctx := context.Background()
	cwd, userDir := t.TempDir(), t.TempDir()
	open := func() *Service {
		svc, err := New(ctx, Config{
			Storage: storage.NewMemory(),
			Model:   llm.NewFakeModel(llm.NewAssistantMessage("done")),
			Paths:   config.Paths{UserDir: userDir},
		})
		if err != nil {
			t.Fatal(err)
		}
		if svc.Storage() != "memory" {
			t.Fatalf("storage = %q", svc.Storage())
		}
		return svc
	}
	first := open()
	if _, err := collect(t, first, userReq("temporary", cwd, "hello")); err != nil {
		t.Fatal(err)
	}
	_ = first.Close()
	second := open()
	defer second.Close()
	list, err := second.ListThreads(ctx, sessions.Filter{Cwd: cwd})
	if err != nil || len(list) != 0 {
		t.Fatalf("threads after restart = %+v, %v", list, err)
	}
}
