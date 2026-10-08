package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tsengmingchun/quill/apps/agent/backend"
	"github.com/tsengmingchun/quill/apps/agent/code/cache"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

// DefaultContext7BaseURL is the Context7 REST API root.
const DefaultContext7BaseURL = "https://context7.com/api"

// DefaultContext7CacheTTL keeps documentation answers; docs change rarely.
const DefaultContext7CacheTTL = time.Hour

const (
	context7MaxBodyBytes = 4 << 20
	context7MaxResults   = 5
)

// Context7Options configures the Context7 tools.
type Context7Options struct {
	APIKey string
	// BaseURL defaults to DefaultContext7BaseURL.
	BaseURL string
	// Client defaults to an http.Client with a 30s timeout.
	Client *http.Client
	// Cache holds responses for CacheTTL (default 1 hour); nil disables it.
	Cache    cache.Cache
	CacheTTL time.Duration
}

const resolveLibraryIDDescription = "Find the Context7 library ID for a library, framework or SDK, so query_docs can fetch its up-to-date documentation. " +
	"Call it before query_docs unless you already have an ID like /owner/repo. Pick the match whose name fits best, preferring more snippets and a higher trust score; " +
	"pin a version (/owner/repo@v1.2.3) when the project uses one."

const queryDocsDescription = "Fetch up-to-date documentation and code examples for a library from Context7. " +
	"Prefer it to web_search for library and API questions: it returns current, version-specific snippets already ranked for your question. " +
	"Ask a specific question (\"how to configure middleware auth in the app router\", not \"auth\")."

// NewContext7Tools returns resolve_library_id and query_docs.
func NewContext7Tools(opts Context7Options) []tool.Tool {
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultContext7BaseURL
	}
	if opts.Client == nil {
		opts.Client = &http.Client{Timeout: 30 * time.Second}
	}
	if opts.CacheTTL <= 0 {
		opts.CacheTTL = DefaultContext7CacheTTL
	}
	c := &context7{opts: opts}
	str := func(desc string) map[string]any {
		return map[string]any{"type": "string", "minLength": 1, "description": desc}
	}
	return []tool.Tool{
		&tool.Func{
			ToolName:        ResolveLibraryIDName,
			ToolEffect:      tool.ReadOnly,
			ToolDescription: resolveLibraryIDDescription,
			ToolSchema: tool.Schema{
				"type": "object",
				"properties": map[string]any{
					"library_name": str("Library name to look up, e.g. \"next.js\" or \"pgx\"."),
					"query":        str("What you are trying to do; used to rank the matches."),
				},
				"required":             []string{"library_name", "query"},
				"additionalProperties": false,
			},
			Fn: func(ctx context.Context, _ *tool.Runtime, args map[string]any) (*tool.Result, error) {
				return c.resolve(ctx, args), nil
			},
		},
		&tool.Func{
			ToolName:        QueryDocsName,
			ToolEffect:      tool.ReadOnly,
			ToolDescription: queryDocsDescription,
			ToolSchema: tool.Schema{
				"type": "object",
				"properties": map[string]any{
					"library_id": str("Context7 library ID from resolve_library_id, e.g. \"/vercel/next.js\" or \"/vercel/next.js@v15.1.8\"."),
					"query":      str("A specific question about the library."),
				},
				"required":             []string{"library_id", "query"},
				"additionalProperties": false,
			},
			Fn: func(ctx context.Context, _ *tool.Runtime, args map[string]any) (*tool.Result, error) {
				return c.query(ctx, args), nil
			},
		},
	}
}

type context7 struct{ opts Context7Options }

type context7Library struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Description    string   `json:"description"`
	TotalSnippets  int      `json:"totalSnippets"`
	TrustScore     float64  `json:"trustScore"`
	BenchmarkScore float64  `json:"benchmarkScore"`
	Versions       []string `json:"versions"`
}

func (c *context7) resolve(ctx context.Context, args map[string]any) *tool.Result {
	name, _ := args["library_name"].(string)
	q, _ := args["query"].(string)
	if strings.TrimSpace(name) == "" || strings.TrimSpace(q) == "" {
		return errResult("Error: resolve_library_id needs library_name and query")
	}
	body, err := c.get(ctx, "/v2/libs/search", url.Values{"libraryName": {name}, "query": {q}})
	if err != nil {
		return errResult("Error: Context7 library search failed: %v", err)
	}
	var resp struct {
		Results []context7Library `json:"results"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return errResult("Error: Context7 library search: decode response: %v", err)
	}
	if len(resp.Results) == 0 {
		return textResult(fmt.Sprintf("No Context7 library matched %q. Try another name, or fall back to web_search.", name))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Context7 libraries for %q (pass the ID to query_docs):\n\n", name)
	for i, l := range resp.Results[:min(len(resp.Results), context7MaxResults)] {
		fmt.Fprintf(&b, "%d. %s — %s\n", i+1, l.Title, l.ID)
		if l.Description != "" {
			fmt.Fprintf(&b, "   %s\n", l.Description)
		}
		fmt.Fprintf(&b, "   Snippets: %d · Trust: %g", l.TotalSnippets, l.TrustScore)
		if l.BenchmarkScore > 0 {
			fmt.Fprintf(&b, " · Benchmark: %g", l.BenchmarkScore)
		}
		b.WriteString("\n")
		if len(l.Versions) > 0 {
			shown := l.Versions[:min(len(l.Versions), 5)]
			fmt.Fprintf(&b, "   Versions: %s (pin one as %s@%s)\n", strings.Join(shown, ", "), l.ID, shown[0])
		}
	}
	return textResult(b.String())
}

func (c *context7) query(ctx context.Context, args map[string]any) *tool.Result {
	id, _ := args["library_id"].(string)
	q, _ := args["query"].(string)
	if !strings.HasPrefix(id, "/") || strings.TrimSpace(q) == "" {
		return errResult("Error: query_docs needs a library_id like /owner/repo (get one with resolve_library_id) and a query")
	}
	body, err := c.get(ctx, "/v2/context", url.Values{"libraryId": {id}, "query": {q}, "type": {"txt"}})
	if err != nil {
		return errResult("Error: Context7 docs for %s failed: %v", id, err)
	}
	text := strings.TrimSpace(string(body))
	if text == "" {
		return textResult(fmt.Sprintf("Context7 has no documentation for %s matching %q.", id, q))
	}
	return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock(backend.TruncateIfTooLong(
		fmt.Sprintf("Context7 docs: %s\nQuery: %s\n\n%s", id, q, text)))}}
}

// get calls the API, serving and filling the cache.
func (c *context7) get(ctx context.Context, path string, q url.Values) ([]byte, error) {
	key := cache.Key("context7", path, q.Encode())
	if c.opts.Cache != nil {
		if v, ok, err := c.opts.Cache.Get(ctx, key); err == nil && ok {
			return v, nil
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.opts.BaseURL, "/")+path+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.opts.APIKey)
	client := *c.opts.Client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, context7MaxBodyBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, context7Error(resp, body)
	}
	if c.opts.Cache != nil {
		_ = c.opts.Cache.Set(ctx, key, body, c.opts.CacheTTL)
	}
	return body, nil
}

func context7Error(resp *http.Response, body []byte) error {
	var e struct {
		Error       string `json:"error"`
		Message     string `json:"message"`
		RedirectURL string `json:"redirectUrl"`
	}
	detail := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &e) == nil && (e.Message != "" || e.Error != "") {
		detail = strings.TrimSpace(e.Error + ": " + e.Message)
	}
	if len(detail) > 500 {
		detail = detail[:500] + "..."
	}
	switch resp.StatusCode {
	case http.StatusAccepted:
		return fmt.Errorf("HTTP 202: the library is still being processed; try again later or use web_search (%s)", detail)
	case http.StatusMovedPermanently, http.StatusFound:
		if e.RedirectURL != "" {
			return fmt.Errorf("HTTP %d: the library moved to %s; call query_docs with that ID", resp.StatusCode, e.RedirectURL)
		}
		return fmt.Errorf("HTTP %d: the library moved (%s)", resp.StatusCode, detail)
	case http.StatusUnauthorized:
		return fmt.Errorf("HTTP 401: CONTEXT7_API_KEY was rejected; keys start with ctx7sk (%s)", detail)
	case http.StatusPaymentRequired:
		return fmt.Errorf("HTTP 402: the Context7 spending limit is reached; use web_search instead (%s)", detail)
	case http.StatusForbidden:
		return fmt.Errorf("HTTP 403: no access to this library on the current plan (%s)", detail)
	case http.StatusNotFound:
		return fmt.Errorf("HTTP 404: library not found; get a valid ID from resolve_library_id (%s)", detail)
	case http.StatusTooManyRequests:
		msg := "HTTP 429: rate limit exceeded"
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			msg += "; retry after " + ra + "s"
		}
		return fmt.Errorf("%s (%s)", msg, detail)
	}
	return fmt.Errorf("HTTP %d (%s)", resp.StatusCode, detail)
}
