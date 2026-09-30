package agent

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"unicode"

	"golang.org/x/net/idna"

	"github.com/tsengmingchun/deepagents_golang/code/config"
	"github.com/tsengmingchun/deepagents_golang/code/tools"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

// WebTools returns fetch_url, plus web_search when a Brave API key is set.
func WebTools(s *config.Settings) []tool.Tool {
	out := []tool.Tool{tools.NewFetchURL()}
	if hasWebSearch(s) {
		out = append([]tool.Tool{tools.NewWebSearch(s.WebSearchAPIKey)}, out...)
	}
	return out
}

func hasWebSearch(s *config.Settings) bool { return s != nil && s.WebSearchAPIKey != "" }

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
	count := "Brave default (20)"
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
	s := fmt.Sprintf("URL: %s\nTimeout: %ss\n\n⚠  Will fetch and convert web content to markdown", stripHidden(raw), timeout)
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
	"- `count` to fetch fewer results; `offset` pages further only when more results are available\n" +
	"- `summary: true` for an AI summary of the results with sources\n" +
	"- `fetch_url` to read a promising page in full\n\n" +
	"Always provide a complete, natural language answer after using web_search.\n\n"

func webSearchSection(opts Options) string {
	if !hasWebSearch(opts.Settings) {
		return ""
	}
	return fmt.Sprintf(webSearchPromptSection, pick(opts.Interactive, " and ask clarifying questions", ""))
}
