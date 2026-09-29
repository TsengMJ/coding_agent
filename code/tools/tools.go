// Package tools holds CLI-only tools: web_search (Tavily) and fetch_url
// with SSRF protection (Python `deepagents_code/tools.py`).
package tools

import (
	"github.com/tsengmingchun/deepagents_golang/internal/stubtool"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

// Tool names.
const (
	WebSearchName = "web_search"
	FetchURLName  = "fetch_url"
)

// NewWebSearch returns the Tavily-backed search tool.
func NewWebSearch(apiKey string) tool.Tool {
	return stubtool.New(WebSearchName, "Search the web for current information.")
}

// NewFetchURL returns the URL fetch tool; it blocks private/loopback IPs and
// converts HTML to markdown.
func NewFetchURL() tool.Tool {
	return stubtool.New(FetchURLName, "Fetch a URL and return its content as markdown.")
}
