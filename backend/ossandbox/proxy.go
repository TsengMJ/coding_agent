package ossandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ProxyOptions configures the allowlist proxy.
type ProxyOptions struct {
	// AllowedDomains are hosts commands may reach: "example.com" matches
	// that host only, "*.example.com" any subdomain of it.
	AllowedDomains []string
	// Blocked rejects resolved addresses commands must not reach, such as
	// private and loopback ranges. Required.
	Blocked func(netip.Addr) bool
	// LookupIP defaults to net.DefaultResolver.
	LookupIP func(ctx context.Context, host string) ([]netip.Addr, error)
	// Dial defaults to a net.Dialer with a 10 second timeout.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// Proxy is an HTTP proxy, outside the sandbox, through which NetworkAllowlist
// commands reach allowed domains. It serves CONNECT tunnels and plain HTTP
// requests on a loopback TCP port and on a unix socket.
type Proxy struct {
	opts     ProxyOptions
	server   *http.Server
	tcp      net.Listener
	unix     net.Listener
	sockDir  string
	closeErr error
	once     sync.Once
}

// StartProxy listens on 127.0.0.1 and a unix socket in a new temp
// directory, and serves until Close.
func StartProxy(opts ProxyOptions) (*Proxy, error) {
	if opts.Blocked == nil {
		return nil, errors.New("sandbox proxy: ProxyOptions.Blocked is required")
	}
	if opts.LookupIP == nil {
		opts.LookupIP = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	if opts.Dial == nil {
		d := &net.Dialer{Timeout: 10 * time.Second}
		opts.Dial = d.DialContext
	}
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("sandbox proxy: %w", err)
	}
	// Unix socket paths are limited to about 100 bytes; /tmp keeps it short.
	dir, err := os.MkdirTemp("/tmp", "quill-proxy-")
	if err != nil {
		tcp.Close()
		return nil, fmt.Errorf("sandbox proxy: %w", err)
	}
	unix, err := net.Listen("unix", filepath.Join(dir, "proxy.sock"))
	if err != nil {
		tcp.Close()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("sandbox proxy: %w", err)
	}
	p := &Proxy{opts: opts, tcp: tcp, unix: unix, sockDir: dir}
	p.server = &http.Server{Handler: p, ReadHeaderTimeout: 30 * time.Second}
	go p.server.Serve(tcp)
	go p.server.Serve(unix)
	return p, nil
}

// Endpoint is where sandboxed commands reach the proxy.
func (p *Proxy) Endpoint() ProxyEndpoint {
	return ProxyEndpoint{Port: p.tcp.Addr().(*net.TCPAddr).Port, Socket: p.unix.Addr().String()}
}

// Close stops the proxy and removes its socket.
func (p *Proxy) Close() error {
	p.once.Do(func() {
		p.closeErr = p.server.Close()
		os.RemoveAll(p.sockDir)
	})
	return p.closeErr
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.tunnel(w, r)
		return
	}
	if r.URL.Scheme != "http" || r.URL.Host == "" {
		http.Error(w, "quill sandbox proxy: only CONNECT and absolute http:// requests are proxied", http.StatusBadRequest)
		return
	}
	host, port := r.URL.Hostname(), r.URL.Port()
	if port == "" {
		port = "80"
	}
	addrs, err := p.check(r.Context(), host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) { pr.Out.URL = pr.In.URL },
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return p.dial(ctx, addrs, port)
			},
			ResponseHeaderTimeout: 60 * time.Second,
		},
	}
	rp.ServeHTTP(w, r)
}

func (p *Proxy) tunnel(w http.ResponseWriter, r *http.Request) {
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		http.Error(w, "quill sandbox proxy: CONNECT needs host:port", http.StatusBadRequest)
		return
	}
	addrs, err := p.check(r.Context(), host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	upstream, err := p.dial(r.Context(), addrs, port)
	if err != nil {
		http.Error(w, fmt.Sprintf("quill sandbox proxy: %v", err), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(w, "quill sandbox proxy: connection cannot be hijacked", http.StatusInternalServerError)
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		client.Close()
		upstream.Close()
		return
	}
	// The client may send TLS bytes before reading the 200.
	if n := buf.Reader.Buffered(); n > 0 {
		pending, _ := buf.Reader.Peek(n)
		if _, err := upstream.Write(pending); err != nil {
			client.Close()
			upstream.Close()
			return
		}
	}
	pipe(client, upstream)
}

// check returns the addresses host may be reached at, or why not.
func (p *Proxy) check(ctx context.Context, host string) ([]netip.Addr, error) {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if !DomainAllowed(p.opts.AllowedDomains, host) {
		return nil, fmt.Errorf("quill sandbox proxy: %s is not in sandbox.allowed_domains", host)
	}
	var addrs []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{ip}
	} else if addrs, err = p.opts.LookupIP(ctx, host); err != nil {
		return nil, fmt.Errorf("quill sandbox proxy: resolve %s: %v", host, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("quill sandbox proxy: %s has no addresses", host)
	}
	for _, ip := range addrs {
		if p.opts.Blocked(ip) {
			return nil, fmt.Errorf("quill sandbox proxy: %s resolves to non-public address %s", host, ip)
		}
	}
	return addrs, nil
}

// dial connects to one of the checked addresses, so a second DNS answer
// cannot redirect the connection (rebinding).
func (p *Proxy) dial(ctx context.Context, addrs []netip.Addr, port string) (net.Conn, error) {
	var lastErr error
	for _, ip := range addrs {
		conn, err := p.opts.Dial(ctx, "tcp", net.JoinHostPort(ip.Unmap().String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// DomainAllowed reports whether host matches one of patterns:
// "example.com" matches only that host, "*.example.com" its subdomains.
func DomainAllowed(patterns []string, host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	for _, pat := range patterns {
		pat = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(pat)), ".")
		if suffix, ok := strings.CutPrefix(pat, "*."); ok {
			if strings.HasSuffix(host, "."+suffix) {
				return true
			}
		} else if pat != "" && host == pat {
			return true
		}
	}
	return false
}

// pipe copies both ways until either side is done, then closes both.
func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	a.Close()
	b.Close()
	<-done
}
