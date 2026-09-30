package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tsengmingchun/deepagents_golang/backend"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

// DefaultBraveBaseURL is the Brave Search API root.
const DefaultBraveBaseURL = "https://api.search.brave.com/res/v1"

// Brave query limits.
const (
	braveMaxQueryChars = 600
	braveMaxQueryWords = 75
	braveMaxGoggles    = 3
	braveMaxBodyBytes  = 8 << 20
	braveRichMaxChars  = 4000
)

// BraveOptions configures the Brave-backed web_search tool.
type BraveOptions struct {
	APIKey string
	// BaseURL defaults to DefaultBraveBaseURL.
	BaseURL string
	// Client defaults to an http.Client with a 30s timeout.
	Client *http.Client
}

// NewBraveSearch returns web_search backed by the Brave Web Search API. Every
// API parameter is a tool argument; unset arguments are not sent, so Brave
// applies its defaults. `summary` and `enable_rich_callback` trigger the
// summarizer and rich-result follow-up calls automatically.
func NewBraveSearch(opts BraveOptions) tool.Tool {
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBraveBaseURL
	}
	if opts.Client == nil {
		opts.Client = &http.Client{Timeout: 30 * time.Second}
	}
	b := &brave{opts: opts}
	return &tool.Func{
		ToolName:        WebSearchName,
		ToolDescription: webSearchDescription,
		ToolSchema:      webSearchSchema(),
		Fn: func(ctx context.Context, _ *tool.Runtime, args map[string]any) (*tool.Result, error) {
			return b.search(ctx, args), nil
		},
	}
}

const webSearchDescription = "Search the web with Brave Search for current information. " +
	"Returns result titles, URLs and snippets, plus news, videos, discussions, FAQ, infobox and " +
	"location results when relevant. Only `query` is required; leave other parameters unset to use " +
	"Brave's defaults. Use `fetch_url` to read a full page."

var (
	braveCountries = []string{"ALL", "AR", "AU", "AT", "BE", "BR", "CA", "CL", "DK", "FI", "FR", "DE", "GR", "HK",
		"IN", "ID", "IE", "IT", "JP", "KR", "MY", "MX", "NL", "NZ", "NO", "CN", "PL", "PT", "PH", "RU", "SA", "ZA",
		"ES", "SE", "CH", "TW", "TR", "GB", "US"}
	braveSearchLangs = []string{"ar", "eu", "bn", "bg", "ca", "zh-hans", "zh-hant", "hr", "cs", "da", "nl", "en",
		"en-gb", "et", "fi", "fr", "gl", "de", "el", "gu", "he", "hi", "hu", "is", "it", "jp", "kn", "ko", "lv", "lt",
		"ms", "ml", "mr", "nb", "pl", "pt-br", "pt-pt", "pa", "ro", "ru", "sr", "sk", "sl", "es", "sv", "ta", "te",
		"th", "tr", "uk", "vi"}
	braveUILangs = []string{"es-AR", "en-AU", "de-AT", "nl-BE", "fr-BE", "pt-BR", "en-CA", "fr-CA", "es-CL",
		"da-DK", "fi-FI", "fr-FR", "de-DE", "el-GR", "zh-HK", "en-IN", "en-ID", "it-IT", "ja-JP", "ko-KR", "en-MY",
		"es-MX", "nl-NL", "en-NZ", "nb-NO", "zh-CN", "pl-PL", "en-PH", "ru-RU", "en-ZA", "es-ES", "sv-SE", "fr-CH",
		"de-CH", "zh-TW", "tr-TR", "en-GB", "en-US", "es-US"}
	braveResultFilters = []string{"discussions", "faq", "infobox", "news", "query", "summarizer", "videos", "web", "locations"}
	braveFreshness     = regexp.MustCompile(`^(pd|pw|pm|py|\d{4}-\d{2}-\d{2}to\d{4}-\d{2}-\d{2})$`)
	braveCountryCode   = regexp.MustCompile(`^[A-Z]{2}$`)
)

type paramKind int

const (
	kindString paramKind = iota
	kindInt
	kindBool
	kindStringList
)

// braveParam is one tool argument sent as a Brave query parameter of the same name.
type braveParam struct {
	name        string
	kind        paramKind
	description string
	enum        []string
	min, max    int
}

var braveQueryParams = []braveParam{
	{name: "country", kind: kindString, enum: braveCountries,
		description: "Country the results come from (2-letter code, or ALL). Brave default: US."},
	{name: "search_lang", kind: kindString, enum: braveSearchLangs,
		description: "Language of the results. Brave default: en."},
	{name: "ui_lang", kind: kindString, enum: braveUILangs,
		description: "Language of the response UI strings (<language>-<country>). Brave default: en-US."},
	{name: "count", kind: kindInt, min: 1, max: 20,
		description: "Number of web results (1-20). Brave default: 20. Fewer results use fewer tokens."},
	{name: "offset", kind: kindInt, min: 0, max: 9,
		description: "Zero-based page of results to skip, in units of `count` (0-9). Only page further when the " +
			"previous response said more results are available."},
	{name: "safesearch", kind: kindString, enum: []string{"off", "moderate", "strict"},
		description: "Adult content filter. Brave default: moderate."},
	{name: "spellcheck", kind: kindBool,
		description: "Let Brave correct the query spelling; the corrected query is reported. Brave default: true."},
	{name: "freshness", kind: kindString,
		description: "Only results discovered in a period: pd (24 hours), pw (7 days), pm (31 days), py (365 days), " +
			"or a range YYYY-MM-DDtoYYYY-MM-DD. Unset means any time."},
	{name: "text_decorations", kind: kindBool,
		description: "Keep highlight markup (e.g. <strong>) in snippets. Brave default: true."},
	{name: "result_filter", kind: kindStringList, enum: braveResultFilters,
		description: "Result types to include. Unset returns every type Brave finds relevant."},
	{name: "units", kind: kindString, enum: []string{"metric", "imperial"},
		description: "Measurement units in results. Brave default: derived from country."},
	{name: "goggles", kind: kindStringList, max: braveMaxGoggles,
		description: "Up to 3 Goggles (URLs or inline definitions) that re-rank results."},
	{name: "extra_snippets", kind: kindBool,
		description: "Return up to 5 extra excerpts per web result. Brave default: false."},
	{name: "summary", kind: kindBool,
		description: "Generate an AI summary of the results; the tool fetches it automatically. Brave default: false."},
	{name: "enable_rich_callback", kind: kindBool,
		description: "Fetch rich results (weather, stocks, sports, calculator ...) when Brave offers them. Brave default: false."},
	{name: "include_fetch_metadata", kind: kindBool,
		description: "Report when each web result was last fetched. Brave default: false."},
	{name: "operators", kind: kindBool,
		description: "Apply search operators such as site:, filetype:, \"exact phrase\" and -term. Brave default: true."},
}

// braveLocationHeaders maps location fields to x-loc-* headers.
var braveLocationHeaders = []struct {
	field, header, description string
}{
	{"lat", "x-loc-lat", "Latitude, -90 to 90."},
	{"long", "x-loc-long", "Longitude, -180 to 180."},
	{"timezone", "x-loc-timezone", "IANA timezone, e.g. America/New_York."},
	{"city", "x-loc-city", "City name."},
	{"state", "x-loc-state", "State or region code (ISO 3166-2 subdivision, up to 3 characters)."},
	{"state_name", "x-loc-state-name", "State or region name."},
	{"country", "x-loc-country", "2-letter country code."},
	{"postal_code", "x-loc-postal-code", "Postal code."},
}

var braveExtraArgs = map[string]string{
	"no_cache":                  "Bypass Brave's cache and search fresh (sends Cache-Control: no-cache).",
	"summary_entity_info":       "With summary: include entity details in the summary.",
	"summary_inline_references": "With summary: add inline source references to the summary text.",
}

func webSearchSchema() tool.Schema {
	props := map[string]any{
		"query": map[string]any{
			"type":      "string",
			"minLength": 1,
			"maxLength": braveMaxQueryChars,
			"description": fmt.Sprintf("The search query, at most %d characters and %d words. Supports operators "+
				"such as site:example.com, filetype:pdf, \"exact phrase\" and -excluded.", braveMaxQueryChars, braveMaxQueryWords),
		},
	}
	for _, p := range braveQueryParams {
		s := map[string]any{"description": p.description}
		switch p.kind {
		case kindString:
			s["type"] = "string"
			if p.enum != nil {
				s["enum"] = p.enum
			}
			if p.name == "freshness" {
				s["pattern"] = braveFreshness.String()
			}
		case kindInt:
			s["type"], s["minimum"], s["maximum"] = "integer", p.min, p.max
		case kindBool:
			s["type"] = "boolean"
		case kindStringList:
			items := map[string]any{"type": "string"}
			if p.enum != nil {
				items["enum"] = p.enum
			}
			s["type"], s["items"], s["minItems"] = "array", items, 1
			if p.max > 0 {
				s["maxItems"] = p.max
			}
		}
		props[p.name] = s
	}
	loc := map[string]any{}
	for _, h := range braveLocationHeaders {
		s := map[string]any{"type": "string", "description": h.description}
		switch h.field {
		case "lat":
			s = map[string]any{"type": "number", "minimum": -90, "maximum": 90, "description": h.description}
		case "long":
			s = map[string]any{"type": "number", "minimum": -180, "maximum": 180, "description": h.description}
		case "country":
			s["pattern"] = braveCountryCode.String()
		}
		loc[h.field] = s
	}
	props["location"] = map[string]any{
		"type":                 "object",
		"description":          "Where the user is, for local results (sent as x-loc-* headers). Set only the fields you know.",
		"properties":           loc,
		"additionalProperties": false,
	}
	for name, desc := range braveExtraArgs {
		props[name] = map[string]any{"type": "boolean", "description": desc}
	}
	return tool.Schema{
		"type":                 "object",
		"properties":           props,
		"required":             []string{"query"},
		"additionalProperties": false,
	}
}

// braveRequest is a validated web_search call.
type braveRequest struct {
	query      url.Values
	header     http.Header
	summary    bool
	rich       bool
	entityInfo bool
	inlineRefs *bool
}

// parseBraveArgs validates args against the schema and maps them to query
// parameters and headers; unset arguments are left out.
func parseBraveArgs(args map[string]any) (*braveRequest, error) {
	known := map[string]bool{"query": true, "location": true}
	for _, p := range braveQueryParams {
		known[p.name] = true
	}
	for name := range braveExtraArgs {
		known[name] = true
	}
	for k := range args {
		if !known[k] {
			return nil, fmt.Errorf("unknown argument %q", k)
		}
	}
	req := &braveRequest{query: url.Values{}, header: http.Header{}}
	q, ok := args["query"].(string)
	if !ok || strings.TrimSpace(q) == "" {
		return nil, errors.New("query must be a non-empty string")
	}
	if n := utf8.RuneCountInString(q); n > braveMaxQueryChars {
		return nil, fmt.Errorf("query is %d characters; the limit is %d", n, braveMaxQueryChars)
	}
	if n := len(strings.Fields(q)); n > braveMaxQueryWords {
		return nil, fmt.Errorf("query is %d words; the limit is %d", n, braveMaxQueryWords)
	}
	req.query.Set("q", q)
	for _, p := range braveQueryParams {
		v, ok := args[p.name]
		if !ok || v == nil {
			continue
		}
		if err := p.apply(req.query, v); err != nil {
			return nil, err
		}
	}
	req.summary = req.query.Get("summary") == "true"
	req.rich = req.query.Get("enable_rich_callback") == "true"
	for name := range braveExtraArgs {
		v, ok := args[name]
		if !ok || v == nil {
			continue
		}
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("%s must be a boolean", name)
		}
		switch name {
		case "no_cache":
			if b {
				req.header.Set("Cache-Control", "no-cache")
			}
		case "summary_entity_info":
			req.entityInfo = b
		case "summary_inline_references":
			req.inlineRefs = &b
		}
	}
	if loc, ok := args["location"]; ok && loc != nil {
		if err := applyLocation(req.header, loc); err != nil {
			return nil, err
		}
	}
	return req, nil
}

func (p braveParam) apply(q url.Values, v any) error {
	switch p.kind {
	case kindString:
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("%s must be a string", p.name)
		}
		if p.enum != nil && !slices.Contains(p.enum, s) {
			return fmt.Errorf("%s must be one of %s, got %q", p.name, strings.Join(p.enum, ", "), s)
		}
		if p.name == "freshness" && !braveFreshness.MatchString(s) {
			return fmt.Errorf("freshness must be pd, pw, pm, py or YYYY-MM-DDtoYYYY-MM-DD, got %q", s)
		}
		q.Set(p.name, s)
	case kindInt:
		n, ok := toInt(v)
		if !ok || n < p.min || n > p.max {
			return fmt.Errorf("%s must be an integer from %d to %d, got %v", p.name, p.min, p.max, v)
		}
		q.Set(p.name, strconv.Itoa(n))
	case kindBool:
		b, ok := v.(bool)
		if !ok {
			return fmt.Errorf("%s must be a boolean", p.name)
		}
		q.Set(p.name, strconv.FormatBool(b))
	case kindStringList:
		items, ok := v.([]any)
		if !ok || len(items) == 0 {
			return fmt.Errorf("%s must be a non-empty array of strings", p.name)
		}
		if p.max > 0 && len(items) > p.max {
			return fmt.Errorf("%s accepts at most %d items, got %d", p.name, p.max, len(items))
		}
		var list []string
		for _, it := range items {
			s, ok := it.(string)
			if !ok || s == "" {
				return fmt.Errorf("%s items must be non-empty strings", p.name)
			}
			if p.enum != nil && !slices.Contains(p.enum, s) {
				return fmt.Errorf("%s items must be one of %s, got %q", p.name, strings.Join(p.enum, ", "), s)
			}
			list = append(list, s)
		}
		if p.name == "result_filter" {
			q.Set(p.name, strings.Join(list, ","))
		} else {
			q[p.name] = list
		}
	}
	return nil
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		if n == math.Trunc(n) {
			return int(n), true
		}
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	}
	return 0, false
}

func applyLocation(h http.Header, v any) error {
	loc, ok := v.(map[string]any)
	if !ok {
		return errors.New("location must be an object")
	}
	fields := map[string]string{}
	for _, lh := range braveLocationHeaders {
		fields[lh.field] = lh.header
	}
	for k, val := range loc {
		header, ok := fields[k]
		if !ok {
			return fmt.Errorf("unknown location field %q", k)
		}
		switch k {
		case "lat", "long":
			f, ok := val.(float64)
			limit := 90.0
			if k == "long" {
				limit = 180
			}
			if !ok || f < -limit || f > limit {
				return fmt.Errorf("location.%s must be a number from %v to %v", k, -limit, limit)
			}
			h.Set(header, strconv.FormatFloat(f, 'f', -1, 64))
		default:
			s, ok := val.(string)
			if !ok || s == "" {
				return fmt.Errorf("location.%s must be a non-empty string", k)
			}
			if k == "country" && !braveCountryCode.MatchString(s) {
				return fmt.Errorf("location.country must be a 2-letter uppercase code, got %q", s)
			}
			h.Set(header, s)
		}
	}
	return nil
}

type brave struct{ opts BraveOptions }

func errResult(format string, a ...any) *tool.Result {
	return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock(fmt.Sprintf(format, a...))}, IsError: true}
}

func (b *brave) search(ctx context.Context, args map[string]any) *tool.Result {
	if b.opts.APIKey == "" {
		return errResult("Error: web_search needs a Brave Search API key. Set BRAVE_API_KEY in the environment or .env and restart.")
	}
	req, err := parseBraveArgs(args)
	if err != nil {
		return errResult("Error: invalid web_search arguments: %v", err)
	}
	var resp braveResponse
	if err := b.get(ctx, "/web/search", req.query, req.header, &resp); err != nil {
		return errResult("Error: Brave search failed: %v", err)
	}
	var out strings.Builder
	var notes []string
	resp.writeQuery(&out)
	if req.summary && resp.Summarizer != nil && resp.Summarizer.Key != "" {
		if err := b.writeSummary(ctx, &out, req, resp.Summarizer.Key); err != nil {
			notes = append(notes, "Note: the summary could not be fetched: "+err.Error())
		}
	}
	if req.rich && resp.Rich != nil && resp.Rich.Hint.CallbackKey != "" {
		if err := b.writeRich(ctx, &out, resp.Rich.Hint.Vertical, resp.Rich.Hint.CallbackKey); err != nil {
			notes = append(notes, "Note: the rich result could not be fetched: "+err.Error())
		}
	}
	resp.writeResults(&out)
	text := strings.TrimSpace(out.String())
	if text == "" {
		text = "No results found for " + strconv.Quote(req.query.Get("q"))
	}
	if len(notes) > 0 {
		text += "\n\n" + strings.Join(notes, "\n")
	}
	return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock(backend.TruncateIfTooLong(text))}}
}

// get performs one Brave API call and decodes the JSON body into out.
func (b *brave) get(ctx context.Context, path string, q url.Values, h http.Header, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(b.opts.BaseURL, "/")+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	for k, v := range h {
		req.Header[k] = v
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", b.opts.APIKey)
	resp, err := b.opts.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, braveMaxBodyBytes))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return braveHTTPError(resp, body)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func braveHTTPError(resp *http.Response, body []byte) error {
	var e struct {
		Error struct {
			Code   string          `json:"code"`
			Detail string          `json:"detail"`
			Meta   json.RawMessage `json:"meta"`
		} `json:"error"`
	}
	detail := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &e) == nil && (e.Error.Detail != "" || e.Error.Code != "") {
		detail = strings.TrimSpace(e.Error.Code + ": " + e.Error.Detail)
		if len(e.Error.Meta) > 0 && string(e.Error.Meta) != "null" {
			detail += " " + string(e.Error.Meta)
		}
	}
	if len(detail) > 1000 {
		detail = detail[:1000] + "..."
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("HTTP %d: the API key was rejected or the plan does not include this feature (%s)", resp.StatusCode, detail)
	case http.StatusUnprocessableEntity:
		return fmt.Errorf("HTTP 422: Brave rejected the parameters (%s)", detail)
	case http.StatusTooManyRequests:
		msg := "HTTP 429: rate limit or quota exceeded"
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			msg += "; retry after " + ra + "s"
		}
		return fmt.Errorf("%s (%s)", msg, detail)
	default:
		return fmt.Errorf("HTTP %d (%s)", resp.StatusCode, detail)
	}
}

func (b *brave) writeSummary(ctx context.Context, out *strings.Builder, req *braveRequest, key string) error {
	q := url.Values{"key": {key}}
	if req.entityInfo {
		q.Set("entity_info", "1")
	}
	if req.inlineRefs != nil {
		q.Set("inline_references", strconv.FormatBool(*req.inlineRefs))
	}
	var s braveSummary
	if err := b.get(ctx, "/summarizer/search", q, nil, &s); err != nil {
		return err
	}
	if s.Status != "" && s.Status != "complete" {
		return fmt.Errorf("summarizer status %q", s.Status)
	}
	text := s.Enrichments.Raw
	if text == "" {
		text = s.tokens()
	}
	if text == "" {
		return errors.New("empty summary")
	}
	out.WriteString("## Summary\n")
	if s.Title != "" {
		out.WriteString(s.Title + "\n")
	}
	out.WriteString(strings.TrimSpace(text) + "\n")
	if len(s.Enrichments.Sources) > 0 {
		out.WriteString("Sources:\n")
		for _, src := range s.Enrichments.Sources {
			fmt.Fprintf(out, "- %s %s\n", src.Title, src.URL)
		}
	}
	if len(s.Followups) > 0 {
		out.WriteString("Follow-up questions: " + strings.Join(s.Followups, " | ") + "\n")
	}
	out.WriteString("\n")
	return nil
}

func (b *brave) writeRich(ctx context.Context, out *strings.Builder, vertical, key string) error {
	var raw json.RawMessage
	if err := b.get(ctx, "/web/rich", url.Values{"callback_key": {key}}, nil, &raw); err != nil {
		return err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return fmt.Errorf("decode rich result: %w", err)
	}
	text := compact.String()
	if len(text) > braveRichMaxChars {
		text = text[:braveRichMaxChars] + "..."
	}
	fmt.Fprintf(out, "## Rich result (%s)\n%s\n\n", vertical, text)
	return nil
}
