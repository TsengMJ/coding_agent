package ossandbox

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// index returns where the consecutive elements want start in args, or -1.
func index(args []string, want ...string) int {
	for i := 0; i+len(want) <= len(args); i++ {
		if slices.Equal(args[i:i+len(want)], want) {
			return i
		}
	}
	return -1
}

func realDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestBwrapArgsOrder(t *testing.T) {
	ws := realDir(t)
	hooks := filepath.Join(ws, "hooks")
	secretDir := filepath.Join(ws, "secret")
	secretFile := filepath.Join(ws, "token")
	for _, d := range []string{hooks, secretDir} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(secretFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := BwrapArgs(Policy{
		Mode:          WorkspaceWrite,
		WritableRoots: []string{ws, filepath.Join(ws, "missing")},
		DenyWrite:     []string{hooks},
		DenyRead:      []string{secretDir, secretFile},
		Network:       NetworkNone,
	})
	root := index(args, "--ro-bind", "/", "/")
	bind := index(args, "--bind", ws, ws)
	ro := index(args, "--ro-bind", hooks, hooks)
	tmpfs := index(args, "--tmpfs", secretDir, "--remount-ro", secretDir)
	null := index(args, "--ro-bind", "/dev/null", secretFile)
	if root != 0 || bind < 0 || ro < 0 || tmpfs < 0 || null < 0 {
		t.Fatalf("missing mounts: %q", args)
	}
	if !(bind < ro && ro < tmpfs) {
		t.Errorf("deny mounts must follow the writable roots: %q", args)
	}
	if index(args, "--unshare-net") < 0 {
		t.Errorf("network none does not unshare the network: %q", args)
	}
	if strings.Contains(strings.Join(args, " "), "missing") {
		t.Errorf("missing path mounted: %q", args)
	}
}

func TestBwrapArgsReadOnlyHasNoWritableBinds(t *testing.T) {
	ws := realDir(t)
	if args := BwrapArgs(Policy{Mode: ReadOnly, WritableRoots: []string{ws}}); index(args, "--bind", ws, ws) >= 0 {
		t.Fatalf("read-only binds a writable root: %q", args)
	}
}

func TestBwrapArgsNetworkFullSharesNetwork(t *testing.T) {
	if args := BwrapArgs(Policy{Mode: ReadOnly, Network: NetworkFull}); index(args, "--unshare-net") >= 0 {
		t.Fatalf("network full unshares the network: %q", args)
	}
}

func TestBwrapAllowlistRunsCommandThroughBridge(t *testing.T) {
	p := Policy{Mode: WorkspaceWrite, Network: NetworkAllowlist, Proxy: ProxyEndpoint{Port: 4321, Socket: "/tmp/q/proxy.sock"}}
	cmd, err := Bwrap{Path: "/usr/bin/bwrap", Bridge: "/opt/quill"}.Command(context.Background(), p, "go mod download")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--", "/opt/quill", NetBridgeArg, "4321", "/tmp/q/proxy.sock", "sh", "-c", "go mod download"}
	if index(cmd.Args, want...) < 0 || index(cmd.Args, "--unshare-net") < 0 {
		t.Fatalf("args = %q", cmd.Args)
	}
}

func TestBwrapCreatesMissingWritableRoots(t *testing.T) {
	cache := filepath.Join(realDir(t), "gopath", "pkg", "sumdb")
	p := Policy{Mode: WorkspaceWrite, WritableRoots: []string{cache}}
	cmd, err := Bwrap{Path: "/usr/bin/bwrap"}.Command(context.Background(), p, "true")
	if err != nil {
		t.Fatal(err)
	}
	if index(cmd.Args, "--bind", cache, cache) < 0 {
		t.Fatalf("missing cache dir not bound writable: %q", cmd.Args)
	}
}

func TestBwrapAllowlistNeedsProxy(t *testing.T) {
	for _, proxy := range []ProxyEndpoint{{}, {Port: 4321}} {
		p := Policy{Mode: WorkspaceWrite, Network: NetworkAllowlist, Proxy: proxy}
		if _, err := (Bwrap{Path: "/usr/bin/bwrap"}).Command(context.Background(), p, "true"); err == nil {
			t.Errorf("proxy %+v: Command succeeded", proxy)
		}
	}
}

// Run by make test-sandbox-linux after removing bubblewrap.
func TestNativeReportsMissingBwrap(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("QUILL_SANDBOX_EXPECT_MISSING") != "1" {
		t.Skip("needs linux without bwrap (QUILL_SANDBOX_EXPECT_MISSING=1)")
	}
	_, err := Native()
	if err == nil || !strings.Contains(err.Error(), "bwrap") {
		t.Fatalf("Native() error = %v, want one naming bwrap", err)
	}
}
