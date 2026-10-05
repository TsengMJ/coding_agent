package tools

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/tsengmingchun/quill/tool"
)

// fakeNet maps test hostnames to addresses and routes every dial to srv,
// recording the address the tool asked for.
func fakeNet(t *testing.T, srv *httptest.Server, hosts map[string]string, maxBytes int64) (tool.Tool, *[]string) {
	t.Helper()
	var dialed []string
	target := srv.Listener.Addr().String()
	return NewFetchURLWith(FetchOptions{
		LookupIP: func(_ context.Context, host string) ([]netip.Addr, error) {
			ip, ok := hosts[host]
			if !ok {
				return nil, fmt.Errorf("no such host %s", host)
			}
			return []netip.Addr{netip.MustParseAddr(ip)}, nil
		},
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialed = append(dialed, addr)
			return (&net.Dialer{}).DialContext(ctx, network, target)
		},
		MaxBytes: maxBytes,
	}), &dialed
}

func TestFetchURLConvertsHTMLToMarkdown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != fetchUserAgent {
			t.Errorf("user agent = %q", r.Header.Get("User-Agent"))
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><body><h1>Title</h1><p>Hello <strong>world</strong>. <a href="/docs">Docs</a></p></body></html>`)
	}))
	defer srv.Close()
	tl, dialed := fakeNet(t, srv, map[string]string{"public.test": "93.184.216.34"}, 0)
	out, isErr := run(t, tl, map[string]any{"url": "http://public.test/page"})
	if isErr {
		t.Fatal(out)
	}
	for _, want := range []string{"URL: http://public.test/page", "Status: 200", "# Title", "Hello **world**.", "[Docs](http://public.test/docs)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if len(*dialed) != 1 || (*dialed)[0] != "93.184.216.34:80" {
		t.Errorf("connection must be pinned to the validated IP, dialed %v", *dialed)
	}
}

func TestFetchURLBlocksNonPublicAddresses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "secret") }))
	defer srv.Close()
	tl, dialed := fakeNet(t, srv, map[string]string{"internal.test": "10.1.2.3", "meta.test": "169.254.169.254"}, 0)
	for _, u := range []string{
		"http://127.0.0.1/", "http://10.0.0.1/", "http://169.254.169.254/latest/meta-data/", "http://[::1]/",
		"http://[::ffff:127.0.0.1]/", "http://internal.test/", "http://meta.test/", "http://0.0.0.0/",
	} {
		out, isErr := run(t, tl, map[string]any{"url": u})
		if !isErr || !strings.Contains(out, "blocked address") {
			t.Errorf("%s: error=%v out=%q", u, isErr, out)
		}
	}
	for _, u := range []string{"file:///etc/passwd", "ftp://example.com/", "http:///nohost"} {
		if out, isErr := run(t, tl, map[string]any{"url": u}); !isErr || !strings.Contains(out, "Fetch URL error") {
			t.Errorf("%s: error=%v out=%q", u, isErr, out)
		}
	}
	if len(*dialed) != 0 {
		t.Fatalf("blocked URLs were dialed: %v", *dialed)
	}
}

func TestFetchURLRevalidatesRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Host {
		case "public.test":
			http.Redirect(w, r, "http://internal.test/admin", http.StatusFound)
		default:
			fmt.Fprint(w, "internal data")
		}
	}))
	defer srv.Close()
	tl, dialed := fakeNet(t, srv, map[string]string{"public.test": "93.184.216.34", "internal.test": "192.168.1.1"}, 0)
	out, isErr := run(t, tl, map[string]any{"url": "http://public.test/"})
	if !isErr || !strings.Contains(out, "blocked address 192.168.1.1") {
		t.Fatalf("error=%v out=%q", isErr, out)
	}
	if len(*dialed) != 1 {
		t.Fatalf("only the first hop may connect, dialed %v", *dialed)
	}
}

func TestFetchURLFollowsSafeRedirectsUpToLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/final" {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "done")
			return
		}
		if r.URL.Path == "/loop" {
			http.Redirect(w, r, "/loop", http.StatusFound)
			return
		}
		http.Redirect(w, r, "/final", http.StatusMovedPermanently)
	}))
	defer srv.Close()
	tl, _ := fakeNet(t, srv, map[string]string{"public.test": "93.184.216.34"}, 0)
	out, isErr := run(t, tl, map[string]any{"url": "http://public.test/start"})
	if isErr || !strings.Contains(out, "URL: http://public.test/final") || !strings.HasSuffix(out, "done") {
		t.Fatalf("error=%v out=%q", isErr, out)
	}
	out, isErr = run(t, tl, map[string]any{"url": "http://public.test/loop"})
	if !isErr || !strings.Contains(out, "exceeded 5 redirects") {
		t.Fatalf("error=%v out=%q", isErr, out)
	}
}

func TestFetchURLCapsResponseSize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, strings.Repeat("x", 100))
	}))
	defer srv.Close()
	tl, _ := fakeNet(t, srv, map[string]string{"public.test": "93.184.216.34"}, 10)
	out, isErr := run(t, tl, map[string]any{"url": "http://public.test/"})
	if isErr || !strings.Contains(out, "exceeded 10 bytes") || !strings.HasSuffix(out, "\n"+strings.Repeat("x", 10)) {
		t.Fatalf("error=%v out=%q", isErr, out)
	}
}

func TestFetchURLErrorsOnHTTPFailureAndBinary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/img" {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte{0x89, 'P', 'N', 'G'})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	tl, _ := fakeNet(t, srv, map[string]string{"public.test": "93.184.216.34"}, 0)
	if out, isErr := run(t, tl, map[string]any{"url": "http://public.test/missing"}); !isErr || !strings.Contains(out, "HTTP 404") {
		t.Errorf("404: error=%v out=%q", isErr, out)
	}
	if out, isErr := run(t, tl, map[string]any{"url": "http://public.test/img"}); !isErr || !strings.Contains(out, "unsupported content type") {
		t.Errorf("binary: error=%v out=%q", isErr, out)
	}
}

func TestBlockedIP(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8": false, "93.184.216.34": false, "2606:4700::1111": false,
		"127.0.0.1": true, "10.0.0.1": true, "172.16.0.1": true, "192.168.0.1": true, "169.254.169.254": true,
		"100.64.0.1": true, "0.0.0.0": true, "224.0.0.1": true, "192.0.2.1": true, "255.255.255.255": true,
		"::1": true, "::": true, "fe80::1": true, "fc00::1": true, "::ffff:10.0.0.1": true,
		"2002:a9fe:a9fe::1": true, "2002:0808:0808::1": false, "2001:db8::1": true,
	} {
		if got := BlockedIP(netip.MustParseAddr(addr)); got != want {
			t.Errorf("BlockedIP(%s) = %v, want %v", addr, got, want)
		}
	}
}
