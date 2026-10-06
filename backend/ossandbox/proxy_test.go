package ossandbox

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestDomainAllowed(t *testing.T) {
	patterns := []string{"github.com", "*.npmjs.org", " PyPI.org. "}
	for host, want := range map[string]bool{
		"github.com":          true,
		"GitHub.com.":         true,
		"api.github.com":      false,
		"registry.npmjs.org":  true,
		"a.b.npmjs.org":       true,
		"npmjs.org":           false,
		"evilnpmjs.org":       false,
		"pypi.org":            true,
		"github.com.evil.com": false,
		"":                    false,
	} {
		if got := DomainAllowed(patterns, host); got != want {
			t.Errorf("DomainAllowed(%q) = %v, want %v", host, got, want)
		}
	}
}

// startTestProxy serves allowed.test and denied.test, both resolving to
// loopback; blocked decides whether loopback counts as non-public.
func startTestProxy(t *testing.T, blocked bool) *Proxy {
	t.Helper()
	p, err := StartProxy(ProxyOptions{
		AllowedDomains: []string{"allowed.test"},
		Blocked:        func(ip netip.Addr) bool { return blocked && ip.IsLoopback() },
		LookupIP: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

// echoServer echoes one line per connection and returns its port.
func echoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				line, _ := bufio.NewReader(c).ReadString('\n')
				io.WriteString(c, "echo "+line)
			}()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	return port
}

// connect sends a CONNECT for target over conn, then "ping\n" through the
// tunnel, and returns the proxy's status and the echoed reply.
func connect(t *testing.T, conn net.Conn, target string) (int, string) {
	t.Helper()
	defer conn.Close()
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, ""
	}
	io.WriteString(conn, "ping\n")
	reply, _ := br.ReadString('\n')
	return resp.StatusCode, reply
}

func dialProxy(t *testing.T, network, addr string) net.Conn {
	t.Helper()
	c, err := net.Dial(network, addr)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestProxyTunnelsAllowedDomain(t *testing.T) {
	p := startTestProxy(t, false)
	port := echoServer(t)
	ep := p.Endpoint()
	for _, conn := range []net.Conn{
		dialProxy(t, "tcp", fmt.Sprintf("127.0.0.1:%d", ep.Port)),
		dialProxy(t, "unix", ep.Socket),
	} {
		if code, reply := connect(t, conn, "allowed.test:"+port); code != http.StatusOK || reply != "echo ping\n" {
			t.Errorf("%s: status %d, reply %q", conn.RemoteAddr().Network(), code, reply)
		}
	}
}

func TestProxyRefusesOtherDomains(t *testing.T) {
	p := startTestProxy(t, false)
	port := echoServer(t)
	conn := dialProxy(t, "tcp", fmt.Sprintf("127.0.0.1:%d", p.Endpoint().Port))
	if code, _ := connect(t, conn, "denied.test:"+port); code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", code)
	}
}

func TestProxyRefusesNonPublicAddresses(t *testing.T) {
	p := startTestProxy(t, true)
	port := echoServer(t)
	conn := dialProxy(t, "tcp", fmt.Sprintf("127.0.0.1:%d", p.Endpoint().Port))
	if code, _ := connect(t, conn, "allowed.test:"+port); code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", code)
	}
}

func TestProxyForwardsPlainHTTP(t *testing.T) {
	p := startTestProxy(t, false)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "host=%s path=%s", r.Host, r.URL.Path)
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	proxyURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", p.Endpoint().Port))
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

	resp, err := client.Get("http://allowed.test:" + port + "/x")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "host=allowed.test:"+port+" path=/x" {
		t.Fatalf("allowed: %d %q", resp.StatusCode, body)
	}

	resp, err = client.Get("http://denied.test:" + port + "/x")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "not in sandbox.allowed_domains") {
		t.Fatalf("denied: %d %q", resp.StatusCode, body)
	}
}

func TestProxyCloseRemovesSocket(t *testing.T) {
	p := startTestProxy(t, false)
	sock := p.Endpoint().Socket
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("socket still exists: %v", err)
	}
}

func TestStartProxyNeedsBlocked(t *testing.T) {
	if _, err := StartProxy(ProxyOptions{AllowedDomains: []string{"x"}}); err == nil {
		t.Fatal("StartProxy without Blocked succeeded")
	}
}

func TestProxyEnv(t *testing.T) {
	p := Policy{Mode: WorkspaceWrite, Network: NetworkAllowlist, Proxy: ProxyEndpoint{Port: 4321}}
	env := p.ProxyEnv()
	for _, want := range []string{"HTTPS_PROXY=http://127.0.0.1:4321", "http_proxy=http://127.0.0.1:4321", "NO_PROXY=localhost,127.0.0.1,::1"} {
		if !slices.Contains(env, want) {
			t.Errorf("ProxyEnv() lacks %q: %q", want, env)
		}
	}
	for _, other := range []Policy{
		{Mode: WorkspaceWrite, Network: NetworkNone, Proxy: p.Proxy},
		{Mode: FullAccess, Network: NetworkAllowlist, Proxy: p.Proxy},
		{Mode: WorkspaceWrite, Network: NetworkAllowlist},
	} {
		if env := other.ProxyEnv(); env != nil {
			t.Errorf("ProxyEnv(%+v) = %q, want none", other, env)
		}
	}
}

func TestNetBridgeReturnsCommandExitCode(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close()
	if code, err := runNetBridge([]string{port, "/nonexistent.sock", "sh", "-c", "exit 3"}); code != 3 || err != nil {
		t.Fatalf("runNetBridge = %d, %v; want 3, nil", code, err)
	}
	if code, _ := runNetBridge([]string{"x"}); code != 2 {
		t.Fatalf("usage error code = %d, want 2", code)
	}
}
