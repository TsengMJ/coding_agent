package ossandbox

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type itEnv struct {
	sb      Sandbox
	ws      string // writable workspace with a git repo
	outside string // existing directory outside every writable root
	secret  string // denied directory holding a fake key
	policy  Policy
}

// newITEnv builds a policy whose only writable root is a fresh workspace,
// so writes anywhere else, including the test's temp dir, must fail.
func newITEnv(t *testing.T) itEnv {
	t.Helper()
	if os.Getenv(ITEnvVar) != "1" {
		t.Skipf("set %s=1 to run sandbox integration tests", ITEnvVar)
	}
	sb, err := Native()
	if err != nil {
		t.Fatalf("Native: %v", err)
	}
	base := t.TempDir()
	e := itEnv{
		sb:      sb,
		ws:      filepath.Join(base, "ws"),
		outside: filepath.Join(base, "outside"),
		secret:  filepath.Join(base, "home", ".ssh"),
	}
	for _, d := range []string{e.ws, e.outside, e.secret} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(e.secret, "id_rsa"), []byte("PRIVATE"), 0o600); err != nil {
		t.Fatal(err)
	}
	git := exec.Command("git", "init", "-q", e.ws)
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	e.policy = Policy{
		Mode:          WorkspaceWrite,
		WritableRoots: []string{e.ws},
		DenyWrite:     []string{filepath.Join(e.ws, ".git", "hooks"), filepath.Join(e.ws, ".git", "config")},
		DenyRead:      []string{e.secret},
		Network:       NetworkNone,
	}
	return e
}

func (e itEnv) run(t *testing.T, p Policy, shellCmd string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd, err := e.sb.Command(ctx, p, shellCmd)
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	cmd.Dir = e.ws
	cmd.Env = append(os.Environ(), p.ProxyEnv()...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (e itEnv) mustRun(t *testing.T, shellCmd string) string {
	t.Helper()
	out, err := e.run(t, e.policy, shellCmd)
	if err != nil {
		t.Fatalf("%s: %v\n%s", shellCmd, err, out)
	}
	return out
}

func (e itEnv) mustFail(t *testing.T, shellCmd string) {
	t.Helper()
	if out, err := e.run(t, e.policy, shellCmd); err == nil {
		t.Fatalf("%s succeeded inside the sandbox:\n%s", shellCmd, out)
	}
}

func TestSandboxWritesWorkspace(t *testing.T) {
	e := newITEnv(t)
	e.mustRun(t, "echo hi > a.txt && mkdir -p sub && echo x > sub/b.txt && cat a.txt")
	if b, err := os.ReadFile(filepath.Join(e.ws, "a.txt")); err != nil || string(b) != "hi\n" {
		t.Fatalf("a.txt = %q, %v", b, err)
	}
}

func TestSandboxBlocksWritesOutsideWorkspace(t *testing.T) {
	e := newITEnv(t)
	e.mustFail(t, "touch "+ShellJoin([]string{filepath.Join(e.outside, "x")}))
	if _, err := os.Stat(filepath.Join(e.outside, "x")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file written outside the workspace: %v", err)
	}
}

func TestSandboxBlocksWritesToHome(t *testing.T) {
	e := newITEnv(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	probe := filepath.Join(home, ".quill-sandbox-probe")
	t.Cleanup(func() { os.Remove(probe) })
	e.mustFail(t, "touch "+ShellJoin([]string{probe}))
}

func TestSandboxBlocksGitHooksAndConfig(t *testing.T) {
	e := newITEnv(t)
	e.mustFail(t, "echo 'echo pwned' > .git/hooks/pre-commit")
	e.mustFail(t, "git config core.hooksPath /tmp")
}

func TestSandboxBlocksSecretReads(t *testing.T) {
	e := newITEnv(t)
	e.mustFail(t, "cat "+ShellJoin([]string{filepath.Join(e.secret, "id_rsa")}))
	// Seatbelt refuses to list the directory; bwrap shows an empty one.
	if out, _ := e.run(t, e.policy, "ls "+ShellJoin([]string{e.secret})); strings.Contains(out, "id_rsa") {
		t.Fatalf("denied directory listed: %s", out)
	}
}

func TestSandboxAllowsGitCommitAndStash(t *testing.T) {
	e := newITEnv(t)
	out := e.mustRun(t, `set -e
g() { git -c user.name=t -c user.email=t@example.com -c commit.gpgsign=false "$@"; }
echo one > f.txt && g add f.txt && g commit -qm one
echo two > f.txt && g stash -q && cat f.txt && g stash pop -q && cat f.txt
g reset -q --hard && cat f.txt`)
	if want := "one\ntwo\none\n"; out != want {
		t.Fatalf("output = %q, want %q", out, want)
	}
}

func TestSandboxBlocksNetwork(t *testing.T) {
	e := newITEnv(t)
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed")
	}
	e.mustFail(t, "curl -sS --max-time 5 -o /dev/null https://example.com")
}

// withAllowlist returns e's policy routed through a real proxy that allows
// only the Go module proxy and checksum database.
func (e itEnv) withAllowlist(t *testing.T) Policy {
	t.Helper()
	proxy, err := StartProxy(ProxyOptions{
		AllowedDomains: []string{"proxy.golang.org", "sum.golang.org"},
		Blocked: func(ip netip.Addr) bool {
			return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { proxy.Close() })
	p := e.policy
	p.Network = NetworkAllowlist
	p.Proxy = proxy.Endpoint()
	return p
}

func TestSandboxAllowlistReachesOnlyAllowedDomains(t *testing.T) {
	e := newITEnv(t)
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed")
	}
	p := e.withAllowlist(t)
	if out, err := e.run(t, p, "curl -sS --max-time 20 -o /dev/null https://proxy.golang.org/"); err != nil {
		t.Fatalf("allowed domain failed: %v\n%s", err, out)
	}
	if out, err := e.run(t, p, "curl -sS --max-time 10 -o /dev/null https://example.com/"); err == nil {
		t.Fatalf("example.com reached through the allowlist:\n%s", out)
	}
	// Bypassing the proxy finds no network at all.
	if out, err := e.run(t, p, "curl -sS --noproxy '*' --max-time 10 -o /dev/null https://proxy.golang.org/"); err == nil {
		t.Fatalf("direct connection succeeded:\n%s", out)
	}
}

func TestSandboxAllowlistGoModDownload(t *testing.T) {
	e := newITEnv(t)
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not installed")
	}
	p := e.withAllowlist(t)
	out, err := e.run(t, p, "GOPATH=$PWD/gopath GOMODCACHE= GOFLAGS=-modcacherw GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org "+
		"go mod download -json rsc.io/quote@v1.5.2")
	if err != nil {
		t.Fatalf("go mod download: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(e.ws, "gopath", "pkg", "mod", "rsc.io", "quote@v1.5.2")); err != nil {
		t.Fatalf("module not downloaded: %v\n%s", err, out)
	}
}

func TestSandboxReadOnlyBlocksWorkspaceWrites(t *testing.T) {
	e := newITEnv(t)
	p := e.policy
	p.Mode = ReadOnly
	if out, err := e.run(t, p, "touch a.txt"); err == nil {
		t.Fatalf("read-only sandbox wrote the workspace:\n%s", out)
	}
	if out, err := e.run(t, p, "ls >/dev/null && cat /dev/null"); err != nil {
		t.Fatalf("read-only sandbox cannot read: %v\n%s", err, out)
	}
}

func TestSandboxFullAccessIsUnconfined(t *testing.T) {
	e := newITEnv(t)
	p := e.policy
	p.Mode = FullAccess
	target := filepath.Join(e.outside, "x")
	if out, err := e.run(t, p, "touch "+ShellJoin([]string{target})); err != nil {
		t.Fatalf("full-access write failed: %v\n%s", err, out)
	}
}

func TestSandboxRunsToolchains(t *testing.T) {
	e := newITEnv(t)
	for _, tool := range []string{"python3", "go"} {
		if _, err := exec.LookPath(tool); err != nil {
			continue
		}
		cmd := tool + " --version"
		if tool == "go" {
			cmd = "go version"
		}
		if out := e.mustRun(t, cmd); !strings.Contains(strings.ToLower(out), strings.TrimSuffix(tool, "3")) {
			t.Errorf("%s output = %q", cmd, out)
		}
	}
}
