package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"golang.org/x/net/idna"

	"github.com/tsengmingchun/deepagents_golang/backend"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

// fetch_url limits.
const (
	maxFetchRedirects     = 5
	DefaultFetchMaxBytes  = 5 << 20
	defaultFetchTimeout   = 30
	maxFetchTimeout       = 120
	fetchUserAgent        = "Mozilla/5.0 (compatible; DeepAgents/1.0)"
	fetchURLDescription   = "Fetch a URL and return the page content as markdown (HTML is converted; plain text is returned as is)."
	fetchURLArgumentDescr = "The URL to fetch (must be a valid HTTP/HTTPS URL)."
)

// FetchOptions configures fetch_url; zero values use the network.
type FetchOptions struct {
	// LookupIP resolves a hostname; defaults to net.DefaultResolver.
	LookupIP func(ctx context.Context, host string) ([]netip.Addr, error)
	// Dial opens the TCP connection to an already validated address.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// MaxBytes caps the response body read (default DefaultFetchMaxBytes).
	MaxBytes int64
}

// NewFetchURLWith returns fetch_url with injected network hooks.
func NewFetchURLWith(opts FetchOptions) tool.Tool {
	if opts.LookupIP == nil {
		opts.LookupIP = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	if opts.Dial == nil {
		opts.Dial = (&net.Dialer{Timeout: 10 * time.Second}).DialContext
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultFetchMaxBytes
	}
	f := &fetcher{opts: opts}
	return &tool.Func{
		ToolName:        FetchURLName,
		ToolDescription: fetchURLDescription,
		ToolSchema: tool.Schema{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{"type": "string", "description": fetchURLArgumentDescr},
				"timeout": map[string]any{
					"type": "integer", "minimum": 1, "maximum": maxFetchTimeout,
					"description": fmt.Sprintf("Request timeout in seconds (default %d).", defaultFetchTimeout),
				},
			},
			"required":             []string{"url"},
			"additionalProperties": false,
		},
		Fn: func(ctx context.Context, _ *tool.Runtime, args map[string]any) (*tool.Result, error) {
			return f.fetch(ctx, args), nil
		},
	}
}

type fetcher struct{ opts FetchOptions }

func (f *fetcher) fetch(ctx context.Context, args map[string]any) *tool.Result {
	raw, ok := args["url"].(string)
	if !ok || raw == "" {
		return errResult("Fetch URL error: url must be a non-empty string")
	}
	timeout := defaultFetchTimeout
	if v, ok := args["timeout"]; ok && v != nil {
		n, ok := toInt(v)
		if !ok || n < 1 || n > maxFetchTimeout {
			return errResult("Fetch URL error: timeout must be an integer from 1 to %d", maxFetchTimeout)
		}
		timeout = n
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	resp, body, truncated, err := f.get(ctx, raw)
	if err != nil {
		return errResult("Fetch URL error: %v (url: %s)", err, raw)
	}
	content, err := toMarkdown(resp, body)
	if err != nil {
		return errResult("Fetch URL error: %v (url: %s)", err, raw)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "URL: %s\nStatus: %d\n", resp.Request.URL, resp.StatusCode)
	if truncated {
		fmt.Fprintf(&b, "Note: the response exceeded %d bytes and was cut off.\n", f.opts.MaxBytes)
	}
	b.WriteString("\n" + content)
	return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock(backend.TruncateIfTooLong(b.String()))}}
}

// get fetches rawURL, re-validating and pinning every redirect hop.
func (f *fetcher) get(ctx context.Context, rawURL string) (*http.Response, []byte, bool, error) {
	current := rawURL
	for range maxFetchRedirects + 1 {
		u, ips, err := f.validate(ctx, current)
		if err != nil {
			return nil, nil, false, err
		}
		resp, err := f.hop(ctx, u, ips)
		if err != nil {
			return nil, nil, false, err
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			resp.Body.Close()
			loc := resp.Header.Get("Location")
			if loc == "" {
				return nil, nil, false, fmt.Errorf("redirect (status %d) at %s is missing a Location header", resp.StatusCode, current)
			}
			next, err := u.Parse(loc)
			if err != nil {
				return nil, nil, false, fmt.Errorf("invalid redirect location %q: %w", loc, err)
			}
			current = next.String()
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, nil, false, fmt.Errorf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, f.opts.MaxBytes+1))
		if err != nil {
			return nil, nil, false, err
		}
		truncated := int64(len(body)) > f.opts.MaxBytes
		if truncated {
			body = body[:f.opts.MaxBytes]
		}
		return resp, body, truncated, nil
	}
	return nil, nil, false, fmt.Errorf("exceeded %d redirects starting from %s", maxFetchRedirects, rawURL)
}

// validate checks scheme and host and returns the addresses the host
// resolves to; any non-public address rejects the URL.
func (f *fetcher) validate(ctx context.Context, raw string) (*url.URL, []netip.Addr, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, nil, fmt.Errorf("URL scheme not allowed: %q (must be http or https)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return nil, nil, errors.New("URL is missing a hostname")
	}
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip}
	} else {
		ascii, err := idna.Lookup.ToASCII(host)
		if err != nil {
			return nil, nil, fmt.Errorf("could not encode hostname %q as IDNA: %w", host, err)
		}
		if ips, err = f.opts.LookupIP(ctx, ascii); err != nil {
			return nil, nil, fmt.Errorf("could not resolve hostname %q: %w", host, err)
		}
		if len(ips) == 0 {
			return nil, nil, fmt.Errorf("hostname %q has no addresses", host)
		}
	}
	for _, ip := range ips {
		if BlockedIP(ip) {
			return nil, nil, fmt.Errorf("URL hostname %q resolves to blocked address %s (private, loopback, link-local or otherwise not public)", host, ip)
		}
	}
	return u, ips, nil
}

// hop sends one request whose connection may only reach the validated ips,
// so a second DNS answer cannot redirect it (rebinding).
func (f *fetcher) hop(ctx context.Context, u *url.URL, ips []netip.Addr) (*http.Response, error) {
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var lastErr error
			for _, ip := range ips {
				conn, err := f.opts.Dial(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			return nil, lastErr
		},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	client := &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", fetchUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	// Idle connections are pinned to this hop's addresses; drop them with the body.
	resp.Body = closeWith{resp.Body, transport.CloseIdleConnections}
	return resp, nil
}

type closeWith struct {
	io.ReadCloser
	after func()
}

func (c closeWith) Close() error {
	err := c.ReadCloser.Close()
	c.after()
	return err
}

// toMarkdown converts HTML bodies to markdown and passes text through.
func toMarkdown(resp *http.Response, body []byte) (string, error) {
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	switch {
	case mediaType == "text/html" || mediaType == "application/xhtml+xml" || mediaType == "" && looksLikeHTML(body):
		md, err := htmltomarkdown.ConvertString(string(body), converter.WithDomain(resp.Request.URL.Scheme+"://"+resp.Request.URL.Host))
		if err != nil {
			return "", fmt.Errorf("convert HTML to markdown: %w", err)
		}
		return strings.TrimSpace(md), nil
	case strings.HasPrefix(mediaType, "text/") || mediaType == "application/json" || strings.HasSuffix(mediaType, "+json") ||
		strings.HasSuffix(mediaType, "+xml") || mediaType == "application/xml" || mediaType == "" && utf8.Valid(body):
		return string(body), nil
	default:
		return "", fmt.Errorf("unsupported content type %q; fetch_url only returns HTML and text", mediaType)
	}
}

func looksLikeHTML(body []byte) bool {
	head := strings.ToLower(strings.TrimSpace(string(body[:min(len(body), 512)])))
	return strings.HasPrefix(head, "<!doctype html") || strings.HasPrefix(head, "<html")
}

// nonPublic lists ranges Python's `ipaddress` treats as not globally
// routable that the net/netip predicates miss.
var nonPublic = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24",
		"203.0.113.0/24", "240.0.0.0/4", "255.255.255.255/32",
		"100::/64", "2001::/23", "2001:db8::/32", "fec0::/10", "64:ff9b:1::/48",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

var sixToFour = netip.MustParsePrefix("2002::/16")

// BlockedIP reports whether ip is not publicly routable: private, loopback,
// link-local (including cloud metadata at 169.254.169.254), multicast,
// unspecified, reserved or documentation ranges. IPv4-mapped and 6to4
// addresses are checked by their embedded IPv4 address.
func BlockedIP(ip netip.Addr) bool {
	ip = ip.WithZone("").Unmap()
	if sixToFour.Contains(ip) {
		b := ip.As16()
		ip = netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]})
	}
	if !ip.IsValid() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, p := range nonPublic {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}
