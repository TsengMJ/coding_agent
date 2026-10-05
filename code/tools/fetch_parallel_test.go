package tools

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tsengmingchun/quill/agent"
	"github.com/tsengmingchun/quill/llm"
	"github.com/tsengmingchun/quill/runtime"
	"github.com/tsengmingchun/quill/tool"
)

// TestFetchURLCallsInOneTurnRunInParallel answers each request only once
// both have arrived, so sequential fetches would time out.
func TestFetchURLCallsInOneTurnRunInParallel(t *testing.T) {
	var arrived sync.WaitGroup
	arrived.Add(2)
	both := make(chan struct{})
	go func() { arrived.Wait(); close(both) }()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived.Done()
		select {
		case <-both:
			fmt.Fprintf(w, "page %s", r.URL.Path)
		case <-time.After(5 * time.Second):
			http.Error(w, "the other fetch never started", http.StatusGatewayTimeout)
		}
	}))
	defer srv.Close()
	fetch := NewFetchURLWith(FetchOptions{
		LookupIP: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		},
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
		},
	})
	model := llm.NewFakeModel(
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{ID: "a", Name: FetchURLName, Args: map[string]any{"url": "http://docs.test/a"}},
			{ID: "b", Name: FetchURLName, Args: map[string]any{"url": "http://docs.test/b"}},
		}},
		llm.NewAssistantMessage("done"),
	)
	a, err := agent.CreateAgent(agent.Config{Model: model, Tools: []tool.Tool{fetch}})
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage("read both")}}, runtime.RunConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range st.Messages {
		if m.Role == llm.RoleTool && (m.Status == llm.ToolStatusError || !strings.Contains(m.Text(), "page /")) {
			t.Fatalf("fetch %s = %q", m.ToolCallID, m.Text())
		}
	}
}
