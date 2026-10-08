package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/tsengmingchun/quill/apps/agent/code/cache"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

// braveServer records requests and serves canned responses keyed by path.
type braveServer struct {
	mu       sync.Mutex
	requests []*http.Request
	routes   map[string]func(w http.ResponseWriter, r *http.Request)
}

func newBraveServer(t *testing.T, routes map[string]func(w http.ResponseWriter, r *http.Request)) (*braveServer, tool.Tool) {
	t.Helper()
	s := &braveServer{routes: routes}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, r)
		s.mu.Unlock()
		if h, ok := s.routes[r.URL.Path]; ok {
			h(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return s, NewBraveSearch(BraveOptions{APIKey: "test-key", BaseURL: srv.URL + "/res/v1", Client: srv.Client()})
}

func jsonReply(v any) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
}

func (s *braveServer) paths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, r := range s.requests {
		out = append(out, r.URL.Path)
	}
	return out
}

func run(t *testing.T, tl tool.Tool, args map[string]any) (string, bool) {
	t.Helper()
	res, err := tl.Invoke(context.Background(), &tool.Runtime{}, args)
	if err != nil {
		t.Fatal(err)
	}
	return res.Content[0].Text, res.IsError
}

var simpleWeb = map[string]any{
	"query": map[string]any{"original": "golang", "more_results_available": true},
	"web": map[string]any{"results": []any{
		map[string]any{"title": "The Go Programming Language", "url": "https://go.dev", "description": "Build simple software.", "age": "2 days ago",
			"extra_snippets": []any{"Go is expressive."}},
	}},
}

func TestBraveMapsEveryArgument(t *testing.T) {
	s, tl := newBraveServer(t, map[string]func(http.ResponseWriter, *http.Request){"/res/v1/web/search": jsonReply(simpleWeb)})
	_, isErr := run(t, tl, map[string]any{
		"query": "golang generics", "country": "DE", "search_lang": "de", "ui_lang": "de-DE",
		"count": float64(5), "offset": float64(2), "safesearch": "strict", "spellcheck": false,
		"freshness": "2024-01-01to2024-06-30", "text_decorations": false,
		"result_filter": []any{"web", "news"}, "units": "metric",
		"goggles":        []any{"https://example.com/a.goggle", "$discard\n$site=example.com"},
		"extra_snippets": true, "include_fetch_metadata": true, "operators": false,
		"location": map[string]any{"lat": 52.52, "long": 13.405, "timezone": "Europe/Berlin", "city": "Berlin",
			"state": "BE", "state_name": "Berlin", "country": "DE", "postal_code": "10115"},
		"no_cache": true,
	})
	if isErr {
		t.Fatal("unexpected error result")
	}
	r := s.requests[0]
	q := r.URL.Query()
	want := url.Values{
		"q": {"golang generics"}, "country": {"DE"}, "search_lang": {"de"}, "ui_lang": {"de-DE"},
		"count": {"5"}, "offset": {"2"}, "safesearch": {"strict"}, "spellcheck": {"false"},
		"freshness": {"2024-01-01to2024-06-30"}, "text_decorations": {"false"},
		"result_filter": {"web,news"}, "units": {"metric"},
		"goggles":        {"https://example.com/a.goggle", "$discard\n$site=example.com"},
		"extra_snippets": {"true"}, "include_fetch_metadata": {"true"}, "operators": {"false"},
	}
	if q.Encode() != want.Encode() {
		t.Errorf("query =\n%s\nwant\n%s", q.Encode(), want.Encode())
	}
	for h, v := range map[string]string{
		"X-Subscription-Token": "test-key", "Accept": "application/json", "Cache-Control": "no-cache",
		"X-Loc-Lat": "52.52", "X-Loc-Long": "13.405", "X-Loc-Timezone": "Europe/Berlin", "X-Loc-City": "Berlin",
		"X-Loc-State": "BE", "X-Loc-State-Name": "Berlin", "X-Loc-Country": "DE", "X-Loc-Postal-Code": "10115",
	} {
		if got := r.Header.Get(h); got != v {
			t.Errorf("header %s = %q, want %q", h, got, v)
		}
	}
}

func TestBraveSendsOnlyGivenArguments(t *testing.T) {
	s, tl := newBraveServer(t, map[string]func(http.ResponseWriter, *http.Request){"/res/v1/web/search": jsonReply(simpleWeb)})
	out, isErr := run(t, tl, map[string]any{"query": "golang"})
	if isErr {
		t.Fatal(out)
	}
	r := s.requests[0]
	if got := r.URL.RawQuery; got != "count=8&q=golang" {
		t.Errorf("query = %q", got)
	}
	if r.Header.Get("Cache-Control") != "" || r.Header.Get("X-Loc-City") != "" {
		t.Errorf("unexpected headers: %v", r.Header)
	}
	if paths := s.paths(); len(paths) != 1 {
		t.Errorf("follow-up calls without summary / rich: %v", paths)
	}
	for _, want := range []string{"Query: golang", "More results available", "1. The Go Programming Language\n   https://go.dev",
		"Build simple software.", "Age: 2 days ago", "- Go is expressive."} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestValidGoggle(t *testing.T) {
	for s, want := range map[string]bool{
		"https://raw.githubusercontent.com/brave/goggles-quickstart/main/goggles/rust_programming.goggle": true,
		"$boost=3,site=go.dev":                 true,
		"! name: docs\n/docs/$boost\n$discard": true,
		"$downrank=2,inurl":                    true,
		"go.dev":                               false,
		"https://go.dev":                       false,
		"$boost=5,site:go.dev":                 false,
		"$site=":                               false,
		"$boost=high":                          false,
	} {
		if got := validGoggle(s); got != want {
			t.Errorf("validGoggle(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestBraveTreatsEmptyValuesAsUnset(t *testing.T) {
	s, tl := newBraveServer(t, map[string]func(http.ResponseWriter, *http.Request){"/res/v1/web/search": jsonReply(simpleWeb)})
	out, isErr := run(t, tl, map[string]any{
		"query": "golang", "country": "", "freshness": "", "units": "",
		"result_filter": []any{}, "goggles": []any{},
		"location": map[string]any{"city": "", "state_name": "", "postal_code": "", "country": ""},
	})
	if isErr {
		t.Fatal(out)
	}
	r := s.requests[0]
	if got := r.URL.RawQuery; got != "count=8&q=golang" {
		t.Errorf("query = %q", got)
	}
	for h := range r.Header {
		if strings.HasPrefix(h, "X-Loc-") {
			t.Errorf("empty location field sent as %s", h)
		}
	}
}

func TestBraveValidatesBeforeSending(t *testing.T) {
	s, tl := newBraveServer(t, nil)
	for name, tc := range map[string]struct {
		args map[string]any
		want string
	}{
		"missing query":   {map[string]any{}, "query must be a non-empty string"},
		"count range":     {map[string]any{"query": "x", "count": float64(21)}, "count must be an integer from 1 to 20"},
		"count fraction":  {map[string]any{"query": "x", "count": 2.5}, "count must be an integer"},
		"offset range":    {map[string]any{"query": "x", "offset": float64(10)}, "offset must be an integer from 0 to 9"},
		"freshness":       {map[string]any{"query": "x", "freshness": "yesterday"}, "freshness must be"},
		"unknown arg":     {map[string]any{"query": "x", "goggles_id": "a"}, `unknown argument "goggles_id"`},
		"too many goggle": {map[string]any{"query": "x", "goggles": []any{"a", "b", "c", "d"}}, "at most 3"},
		"filter enum":     {map[string]any{"query": "x", "result_filter": []any{"images"}}, "result_filter items must be one of"},
		"country enum":    {map[string]any{"query": "x", "country": "XX"}, "country must be one of"},
		"long query":      {map[string]any{"query": strings.Repeat("a", 601)}, "601 characters"},
		"many words":      {map[string]any{"query": strings.Repeat("a ", 76)}, "76 words"},
		"bad lat":         {map[string]any{"query": "x", "location": map[string]any{"lat": float64(91)}}, "location.lat"},
		"location field":  {map[string]any{"query": "x", "location": map[string]any{"zip": "1"}}, `unknown location field "zip"`},
		"bool type":       {map[string]any{"query": "x", "summary": "yes"}, "summary must be a boolean"},
		"goggle site":     {map[string]any{"query": "x", "goggles": []any{"go.dev"}}, "put site:example.com in query"},
		"goggle page url": {map[string]any{"query": "x", "goggles": []any{"https://go.dev"}}, "put site:example.com in query"},
		"goggle syntax":   {map[string]any{"query": "x", "goggles": []any{"$boost=5,site:go.dev"}}, "use '=', not ':'"},
	} {
		out, isErr := run(t, tl, tc.args)
		if !isErr || !strings.Contains(out, tc.want) {
			t.Errorf("%s: error=%v out=%q, want %q", name, isErr, out, tc.want)
		}
	}
	if len(s.requests) != 0 {
		t.Fatalf("invalid calls reached Brave: %d", len(s.requests))
	}
}

func TestBraveMissingKey(t *testing.T) {
	out, isErr := run(t, NewWebSearch(""), map[string]any{"query": "x"})
	if !isErr || !strings.Contains(out, "BRAVE_API_KEY") {
		t.Fatalf("out = %q", out)
	}
}

func TestBraveHTTPErrorsBecomeErrorResults(t *testing.T) {
	for status, want := range map[int]string{
		401: "API key was rejected",
		403: "plan does not include",
		422: "Brave rejected the parameters",
		429: "retry after 3s",
	} {
		_, tl := newBraveServer(t, map[string]func(http.ResponseWriter, *http.Request){
			"/res/v1/web/search": func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "3")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"type":"ErrorResponse","error":{"code":"X","detail":"bad things","meta":{"errors":[{"loc":["count"]}]}}}`))
			},
		})
		out, isErr := run(t, tl, map[string]any{"query": "x"})
		if !isErr || !strings.Contains(out, want) || !strings.Contains(out, "bad things") {
			t.Errorf("%d: error=%v out=%q", status, isErr, out)
		}
	}
}

func TestBraveSummaryFollowUp(t *testing.T) {
	web := map[string]any{"query": map[string]any{"original": "q"}, "summarizer": map[string]any{"key": "sum-key"},
		"web": map[string]any{"results": []any{map[string]any{"title": "T", "url": "https://t"}}}}
	s, tl := newBraveServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/res/v1/web/search": jsonReply(web),
		"/res/v1/summarizer/search": jsonReply(map[string]any{
			"status": "complete", "title": "About q",
			"enrichments": map[string]any{"raw": "Q is a letter.", "sources": []any{map[string]any{"title": "Wiki", "url": "https://w"}}},
			"followups":   []any{"What is R?"},
		}),
	})
	out, isErr := run(t, tl, map[string]any{"query": "q", "summary": true, "summary_entity_info": true, "summary_inline_references": true})
	if isErr {
		t.Fatal(out)
	}
	if got := s.requests[0].URL.Query().Get("summary"); got != "true" {
		t.Errorf("summary param = %q", got)
	}
	sq := s.requests[1].URL.Query()
	if s.requests[1].URL.Path != "/res/v1/summarizer/search" || sq.Get("key") != "sum-key" || sq.Get("entity_info") != "1" ||
		sq.Get("inline_references") != "true" || s.requests[1].Header.Get("X-Subscription-Token") != "test-key" {
		t.Errorf("summarizer request = %s %v", s.requests[1].URL, s.requests[1].Header)
	}
	summary, web1 := strings.Index(out, "## Summary"), strings.Index(out, "## Web results")
	if summary < 0 || web1 < summary {
		t.Fatalf("summary must come before web results:\n%s", out)
	}
	for _, want := range []string{"About q", "Q is a letter.", "- Wiki https://w", "Follow-up questions: What is R?"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestBraveSummaryFailureOnlyAddsNote(t *testing.T) {
	web := map[string]any{"summarizer": map[string]any{"key": "k"}, "web": map[string]any{"results": []any{map[string]any{"title": "T", "url": "https://t"}}}}
	_, tl := newBraveServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/res/v1/web/search":        jsonReply(web),
		"/res/v1/summarizer/search": jsonReply(map[string]any{"status": "failed"}),
	})
	out, isErr := run(t, tl, map[string]any{"query": "q", "summary": true})
	if isErr || !strings.Contains(out, "https://t") || !strings.Contains(out, "Note: the summary could not be fetched") {
		t.Fatalf("error=%v out=%q", isErr, out)
	}
}

func TestBraveNotesWhenRequestedExtrasAreMissing(t *testing.T) {
	s, tl := newBraveServer(t, map[string]func(http.ResponseWriter, *http.Request){"/res/v1/web/search": jsonReply(simpleWeb)})
	out, isErr := run(t, tl, map[string]any{"query": "q", "summary": true, "enable_rich_callback": true})
	if isErr || len(s.requests) != 1 {
		t.Fatalf("error=%v requests=%d out=%q", isErr, len(s.requests), out)
	}
	for _, want := range []string{"Note: Brave returned no summary", "Note: Brave offered no rich result"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestBraveRichCallback(t *testing.T) {
	web := map[string]any{"rich": map[string]any{"type": "rich", "hint": map[string]any{"vertical": "weather", "callback_key": "cb"}}}
	s, tl := newBraveServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/res/v1/web/search": jsonReply(web),
		"/res/v1/web/rich":   jsonReply(map[string]any{"type": "rich", "results": []any{map[string]any{"temp": 21}}}),
	})
	out, isErr := run(t, tl, map[string]any{"query": "weather berlin", "enable_rich_callback": true})
	if isErr {
		t.Fatal(out)
	}
	if got := s.requests[1].URL.Query().Get("callback_key"); got != "cb" {
		t.Errorf("callback_key = %q", got)
	}
	if !strings.Contains(out, "## Rich result (weather)") || !strings.Contains(out, `"temp":21`) {
		t.Fatalf("out = %q", out)
	}
}

func TestBraveFormatsOtherResultTypes(t *testing.T) {
	resp := map[string]any{
		"news": map[string]any{"results": []any{map[string]any{"title": "N", "url": "https://n", "age": "1h", "meta_url": map[string]any{"hostname": "n.com"}}}},
		"faq":  map[string]any{"results": []any{map[string]any{"question": "Why?", "answer": "Because.", "title": "F", "url": "https://f"}}},
		"locations": map[string]any{"results": []any{map[string]any{"title": "Cafe", "postal_address": map[string]any{"displayAddress": "1 Main St"},
			"rating": map[string]any{"ratingValue": 4.5, "reviewCount": 10}, "contact": map[string]any{"telephone": "123"}}}},
		"infobox": map[string]any{"results": []any{map[string]any{"title": "Go", "long_desc": "A language.", "attributes": []any{[]any{"Designer", "Rob Pike"}}}}},
	}
	_, tl := newBraveServer(t, map[string]func(http.ResponseWriter, *http.Request){"/res/v1/web/search": jsonReply(resp)})
	out, _ := run(t, tl, map[string]any{"query": "q"})
	order := []string{"## News", "n.com · 1h", "## FAQ", "Q: Why?\nA: Because.", "## Infobox", "Designer: Rob Pike", "## Locations",
		"1 Main St", "Rating: 4.5 (10 reviews)", "Phone: 123"}
	last := -1
	for _, want := range order {
		i := strings.Index(out, want)
		if i < last {
			t.Fatalf("%q missing or out of order:\n%s", want, out)
		}
		last = i
	}
}

func TestBraveTruncatesLongOutput(t *testing.T) {
	var results []any
	for range 400 {
		results = append(results, map[string]any{"title": strings.Repeat("t", 200), "url": "https://x", "description": strings.Repeat("d", 400)})
	}
	_, tl := newBraveServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/res/v1/web/search": jsonReply(map[string]any{"web": map[string]any{"results": results}}),
	})
	out, _ := run(t, tl, map[string]any{"query": "q"})
	if len(out) > 150_000 || !strings.Contains(out, "truncated") {
		t.Fatalf("len = %d, tail = %q", len(out), out[len(out)-200:])
	}
}

func TestWebSearchSchemaIsClosed(t *testing.T) {
	s := NewWebSearch("k").Schema()
	if s["additionalProperties"] != false {
		t.Fatal("schema must reject unknown arguments")
	}
	props := s["properties"].(map[string]any)
	for _, name := range []string{"query", "country", "search_lang", "ui_lang", "count", "offset", "safesearch", "spellcheck",
		"freshness", "text_decorations", "result_filter", "units", "goggles", "extra_snippets", "summary",
		"enable_rich_callback", "include_fetch_metadata", "operators", "location", "no_cache",
		"summary_entity_info", "summary_inline_references"} {
		if _, ok := props[name]; !ok {
			t.Errorf("schema missing %q", name)
		}
	}
	if _, ok := props["goggles_id"]; ok {
		t.Error("deprecated goggles_id must not be exposed")
	}
}

func TestBraveDomainFiltersBecomeSiteOperators(t *testing.T) {
	s, tl := newBraveServer(t, map[string]func(http.ResponseWriter, *http.Request){"/res/v1/web/search": jsonReply(simpleWeb)})
	out, isErr := run(t, tl, map[string]any{"query": "context api", "allowed_domains": []any{"go.dev", "pkg.go.dev"},
		"blocked_domains": []any{"w3schools.com"}})
	if isErr {
		t.Fatal(out)
	}
	if got := s.requests[0].URL.Query().Get("q"); got != "context api (site:go.dev OR site:pkg.go.dev) -site:w3schools.com" {
		t.Fatalf("q = %q", got)
	}
}

func TestBraveRejectsBadDomains(t *testing.T) {
	_, tl := newBraveServer(t, nil)
	for _, d := range []any{"https://go.dev/doc", "go dev", ""} {
		out, isErr := run(t, tl, map[string]any{"query": "x", "allowed_domains": []any{d}})
		if !isErr || !strings.Contains(out, "domain") {
			t.Errorf("%q: %v %s", d, isErr, out)
		}
	}
}

func TestBraveCachesResults(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		jsonReply(simpleWeb)(w, r)
	}))
	defer srv.Close()
	tl := NewBraveSearch(BraveOptions{APIKey: "k", BaseURL: srv.URL + "/res/v1", Client: srv.Client(), Cache: cache.NewMemory()})
	first, _ := run(t, tl, map[string]any{"query": "golang"})
	second, _ := run(t, tl, map[string]any{"query": "golang"})
	if hits != 1 || first != second {
		t.Fatalf("hits = %d", hits)
	}
	run(t, tl, map[string]any{"query": "golang", "count": float64(3)})
	run(t, tl, map[string]any{"query": "golang", "no_cache": true})
	if hits != 3 {
		t.Fatalf("different arguments or no_cache reused the cache: hits = %d", hits)
	}
}
