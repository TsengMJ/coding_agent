package agent

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"unicode"

	"golang.org/x/net/idna"

	"github.com/tsengmingchun/deepagents_golang/backend"
	"github.com/tsengmingchun/deepagents_golang/code/cache"
	"github.com/tsengmingchun/deepagents_golang/code/config"
	"github.com/tsengmingchun/deepagents_golang/code/tools"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

// WebOptions configures WebTools.
type WebOptions struct {
	Settings *config.Settings
	// Extractor answers fetch_url prompts; nil leaves prompt out.
	Extractor llm.ChatModel
	// Pages keeps fetched pages, shown to file tools under WebDir; nil
	// keeps none.
	Pages backend.Backend
	// Cache holds fetch answers and search results; nil disables caching.
	Cache cache.Cache
}

// WebTools returns fetch_url, plus web_search when a Brave API key is set
// and the Context7 tools when a Context7 API key is set.
func WebTools(o WebOptions) []tool.Tool {
	var out []tool.Tool
	if hasWebSearch(o.Settings) {
		out = append(out, tools.NewBraveSearch(tools.BraveOptions{APIKey: o.Settings.WebSearchAPIKey, Cache: o.Cache}))
	}
	out = append(out, tools.NewFetchURLWith(tools.FetchOptions{
		Extractor: o.Extractor, Pages: o.Pages, PagesDir: WebDir, Cache: o.Cache,
	}))
	if hasContext7(o.Settings) {
		out = append(out, tools.NewContext7Tools(tools.Context7Options{APIKey: o.Settings.Context7APIKey, Cache: o.Cache})...)
	}
	return out
}

func hasWebSearch(s *config.Settings) bool { return s != nil && s.WebSearchAPIKey != "" }

func hasContext7(s *config.Settings) bool { return s != nil && s.Context7APIKey != "" }

// FormatContext7Description renders the approval text for a Context7 call.
func FormatContext7Description(call llm.ToolCall) string {
	var b strings.Builder
	if name, _ := call.Args["library_name"].(string); name != "" {
		fmt.Fprintf(&b, "Library: %s\n", name)
	}
	if id, _ := call.Args["library_id"].(string); id != "" {
		fmt.Fprintf(&b, "Library ID: %s\n", id)
	}
	q, _ := call.Args["query"].(string)
	fmt.Fprintf(&b, "Query: %s\n\n⚠  This will send the query to Context7 and use API credits", q)
	return b.String()
}

// webSearchDefaultArgs are the arguments summarized on the first line of the
// approval description rather than listed as extras.
var webSearchDefaultArgs = []string{"query", "count"}

// FormatWebSearchDescription renders the approval text for a web_search
// call (`_format_web_search_description`), listing every non-default argument.
func FormatWebSearchDescription(call llm.ToolCall) string {
	query, _ := call.Args["query"].(string)
	if query == "" {
		query = "unknown"
	}
	count := fmt.Sprintf("%d (default)", tools.DefaultWebSearchCount)
	if v, ok := call.Args["count"]; ok && v != nil {
		count = fmt.Sprint(v)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Query: %s\nMax results: %s\n", query, count)
	var keys []string
	for k := range call.Args {
		if !slices.Contains(webSearchDefaultArgs, k) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s: %s\n", k, formatArg(call.Args[k]))
	}
	followups := ""
	if call.Args["summary"] == true || call.Args["enable_rich_callback"] == true {
		followups = " (plus follow-up summary / rich result calls)"
	}
	fmt.Fprintf(&b, "\n⚠  This will use Brave Search API credits%s", followups)
	return b.String()
}

func formatArg(v any) string {
	switch x := v.(type) {
	case []any:
		parts := make([]string, len(x))
		for i, p := range x {
			parts[i] = fmt.Sprint(p)
		}
		return strings.Join(parts, ", ")
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = fmt.Sprintf("%s=%v", k, x[k])
		}
		return strings.Join(parts, ", ")
	default:
		return fmt.Sprint(v)
	}
}

// FormatFetchURLDescription renders the approval text for a fetch_url call
// (`_format_fetch_url_description`), warning about hidden characters and
// punycode domains.
func FormatFetchURLDescription(call llm.ToolCall) string {
	raw, _ := call.Args["url"].(string)
	if raw == "" {
		raw = "unknown"
	}
	timeout := "30"
	if v, ok := call.Args["timeout"]; ok && v != nil {
		timeout = fmt.Sprint(v)
	}
	s := fmt.Sprintf("URL: %s\nTimeout: %ss\n", stripHidden(raw), timeout)
	if prompt, _ := call.Args["prompt"].(string); prompt != "" {
		s += fmt.Sprintf("Prompt: %s\n", prompt)
	}
	s += "\n⚠  Will fetch and convert web content to markdown"
	for _, w := range urlWarnings(raw) {
		s += "\n⚠  " + w
	}
	return s
}

// hidden reports format characters (zero-width, bidi overrides) that make a
// URL render differently from what is fetched.
func hidden(r rune) bool { return unicode.Is(unicode.Cf, r) }

func stripHidden(s string) string {
	return strings.Map(func(r rune) rune {
		if hidden(r) {
			return -1
		}
		return r
	}, s)
}

func urlWarnings(raw string) []string {
	var out []string
	if strings.ContainsFunc(raw, hidden) {
		out = append(out, "URL warning: URL contains hidden Unicode characters")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return out
	}
	host := u.Hostname()
	if decoded, err := idna.Display.ToUnicode(host); err == nil && decoded != host {
		out = append(out, "Decoded domain: "+decoded)
	} else if err != nil && strings.Contains(host, "xn--") {
		out = append(out, "URL warning: punycode domain could not be decoded")
	}
	return out
}

const webSearchPromptSection = "### Web Search Tool Usage\n\n" +
	"When you use the web_search tool:\n\n" +
	"1. The tool will return search results with titles, URLs, and content excerpts\n" +
	"2. You MUST read and process these results, then respond naturally to the user\n" +
	"3. NEVER show raw JSON or tool results directly to the user\n" +
	"4. Synthesize the information from multiple sources into a coherent answer\n" +
	"5. Cite your sources by mentioning page titles or URLs when relevant\n" +
	"6. If the search doesn't find what you need, explain what you found%s\n\n" +
	"Search parameters (leave unset unless they help):\n" +
	"- `freshness` (pd / pw / pm / py or a date range) for recent events\n" +
	"- `result_filter` to restrict result types, e.g. [\"news\"] or [\"web\", \"faq\"]\n" +
	"- `count` (default 8) to fetch fewer or more results; `offset` pages further only when more results are available\n" +
	"- `allowed_domains` / `blocked_domains` to keep to official documentation or drop low-quality sites\n" +
	"- `summary: true` for an AI summary of the results with sources\n\n" +
	"Always provide a complete, natural language answer after using web_search.\n\n"

// webSearchSection tells the model where to look things up, in order of
// reliability and cost, for the web tools it has.
func webSearchSection(opts Options) string {
	if opts.NoWebTools {
		return ""
	}
	steps := []string{"The project itself: read the code and the dependency manifests (go.mod, package.json, lockfiles, ...) to learn which versions are in use."}
	if hasContext7(opts.Settings) {
		steps = append(steps, "Context7 for library, framework and API documentation: `resolve_library_id`, then `query_docs` with the version the project uses pinned (/owner/repo@version).")
	}
	if hasWebSearch(opts.Settings) {
		steps = append(steps, "`web_search` for everything else (error messages, recent releases, comparisons); use `allowed_domains` to stay on official sources.")
	}
	steps = append(steps, "`fetch_url` to read a specific page. Pass a `prompt` saying exactly what you need, so only the answer comes back; the full page is saved, so read_file or grep it when the answer is not enough.")
	var b strings.Builder
	b.WriteString("### Looking Things Up\n\n")
	if hasWebResearcher(opts) {
		fmt.Fprintf(&b, "Delegate any lookup that needs more than one search or page to the `%s` subagent with `task`: it returns a short, cited report and keeps the pages out of your context. "+
			"Send independent questions to separate researchers in one message, and set `run_in_background: true` when you have other work to do meanwhile. "+
			"Once a question is delegated, do not search for it yourself; wait for the report.\n\n"+
			"Look things up yourself only for a single quick check, in this order, and stop once you have a reliable answer:\n", WebResearcherName)
	} else {
		b.WriteString("Check sources in this order and stop once you have a reliable answer:\n")
	}
	for i, s := range steps {
		fmt.Fprintf(&b, "%d. %s\n", i+1, s)
	}
	b.WriteString("\n")
	if hasWebSearch(opts.Settings) {
		fmt.Fprintf(&b, webSearchPromptSection, pick(opts.Interactive, " and ask clarifying questions", ""))
	}
	return b.String()
}
