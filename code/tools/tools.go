// Package tools holds CLI-only tools: web_search (Brave Search, where
// Python uses Tavily) and fetch_url with SSRF protection (Python
// `deepagents_code/tools.py`).
package tools

import "github.com/tsengmingchun/deepagents_golang/tool"

// Tool names.
const (
	WebSearchName = "web_search"
	FetchURLName  = "fetch_url"
)

// NewWebSearch returns the Brave-backed search tool.
func NewWebSearch(apiKey string) tool.Tool {
	return NewBraveSearch(BraveOptions{APIKey: apiKey})
}

// NewFetchURL returns the URL fetch tool; it blocks non-public IPs, pins
// each connection to the validated address and converts HTML to markdown.
func NewFetchURL() tool.Tool {
	return NewFetchURLWith(FetchOptions{})
}
