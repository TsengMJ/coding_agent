package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenRouterSendsSlugAndAttribution(t *testing.T) {
	t.Setenv("OPENROUTER_APP_TITLE", "")
	var gotBody string
	var gotHeader http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody, gotHeader = string(raw), r.Header.Clone()
		_, _ = io.WriteString(w, textReply)
	}))
	defer srv.Close()

	t.Setenv("OPENROUTER_API_KEY", "sk-or-test")
	t.Setenv("OPENROUTER_API_BASE", srv.URL)
	m, err := InitChatModel("openrouter:openai/gpt-6-luna", nil)
	if err != nil {
		t.Fatal(err)
	}
	if info := m.Info(); info.Provider != "openrouter" || info.Model != "openai/gpt-6-luna" {
		t.Fatalf("Info() = %+v", info)
	}
	if _, err := m.Generate(context.Background(), ChatRequest{Messages: []Message{NewUserMessage("hi")}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotBody, `"model":"openai/gpt-6-luna"`) {
		t.Errorf("body missing model slug: %s", gotBody)
	}
	if gotHeader.Get("Authorization") != "Bearer sk-or-test" {
		t.Errorf("Authorization = %q", gotHeader.Get("Authorization"))
	}
	if gotHeader.Get("HTTP-Referer") != defaultOpenRouterAppURL {
		t.Errorf("HTTP-Referer = %q", gotHeader.Get("HTTP-Referer"))
	}
	if _, set := gotHeader["X-Title"]; set {
		t.Errorf("empty OPENROUTER_APP_TITLE should omit X-Title, got %q", gotHeader.Get("X-Title"))
	}
}

func TestOpenRouterRequiresAPIKey(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	_, err := InitChatModel("openrouter:openai/gpt-6-luna", nil)
	if err == nil || !strings.Contains(err.Error(), "OPENROUTER_API_KEY") {
		t.Fatalf("want missing key error, got %v", err)
	}
}
