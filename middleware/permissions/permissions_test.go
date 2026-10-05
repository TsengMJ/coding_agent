package permissions

import (
	"slices"
	"strings"
	"testing"

	"github.com/tsengmingchun/quill/llm"
)

func rule(mode Mode, ops []Operation, paths ...string) FilesystemPermission {
	return FilesystemPermission{Operations: ops, Paths: paths, Mode: mode}
}

var (
	read  = []Operation{OpRead}
	write = []Operation{OpWrite}
	both  = []Operation{OpRead, OpWrite}
)

func TestValidateRejectsBadPaths(t *testing.T) {
	for _, tc := range []struct {
		rule FilesystemPermission
		want string
	}{
		{rule(ModeDeny, read, "secrets/**"), "must start with '/'"},
		{rule(ModeDeny, read, "/a/../b"), "'..'"},
		{rule(ModeDeny, read, "/~/x"), "'~'"},
		{rule("block", read, "/x"), "mode"},
		{rule(ModeDeny, []Operation{"delete"}, "/x"), "operation"},
		{rule(ModeDeny, read, "/[a"), "invalid"},
	} {
		err := tc.rule.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: err = %v, want %q", tc.rule, err, tc.want)
		}
	}
	if err := rule(ModeDeny, both, "/secrets/**", "/{a,b}/*.env").Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCheckFirstMatchWins(t *testing.T) {
	rules := []FilesystemPermission{
		rule(ModeAllow, read, "/secrets/public.txt"),
		rule(ModeDeny, both, "/secrets/**"),
		rule(ModeInterrupt, write, "/config/**"),
	}
	for _, tc := range []struct {
		op   Operation
		path string
		want Mode
	}{
		{OpRead, "/secrets/public.txt", ModeAllow},
		{OpWrite, "/secrets/public.txt", ModeDeny},
		{OpRead, "/secrets/a/b.txt", ModeDeny},
		{OpRead, "/secrets/.env", ModeDeny},
		{OpRead, "/secrets/", ModeDeny},
		{OpWrite, "/config/app.yaml", ModeInterrupt},
		{OpRead, "/config/app.yaml", ModeAllow},
		{OpRead, "/other.txt", ModeAllow},
	} {
		if got := Check(rules, tc.op, tc.path); got != tc.want {
			t.Errorf("Check(%s, %s) = %s, want %s", tc.op, tc.path, got, tc.want)
		}
	}
}

func TestFilterPathsDropsOnlyDenied(t *testing.T) {
	rules := []FilesystemPermission{
		rule(ModeDeny, read, "/secrets/**", "/secrets"),
		rule(ModeInterrupt, read, "/review/**"),
	}
	got := FilterPaths(rules, OpRead, []string{"/a.txt", "/secrets/", "/secrets/k", "/review/x"})
	if want := []string{"/a.txt", "/review/x"}; !slices.Equal(got, want) {
		t.Fatalf("FilterPaths = %v, want %v", got, want)
	}
}

func TestGlobAnchorAndOverlap(t *testing.T) {
	for pattern, want := range map[string]string{
		"/secrets/**":     "/secrets",
		"/a/b/*.txt":      "/a/b",
		"/**/x":           "/",
		"/{a,b}/c":        "/",
		"/exact/file.txt": "/exact/file.txt",
	} {
		if got := GlobAnchor(pattern); got != want {
			t.Errorf("GlobAnchor(%q) = %q, want %q", pattern, got, want)
		}
	}
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"/", "/x", true},
		{"/a", "/a/b", true},
		{"/a/b", "/a", true},
		{"/ab", "/a", false},
		{"/x", "/y", false},
	} {
		if got := PathsOverlap(tc.a, tc.b); got != tc.want {
			t.Errorf("PathsOverlap(%q, %q) = %v", tc.a, tc.b, got)
		}
	}
}

func TestDeleteDenyPatterns(t *testing.T) {
	rules := []FilesystemPermission{
		rule(ModeAllow, write, "/data/**"),
		rule(ModeDeny, write, "/data/keep/**"),
		rule(ModeDeny, write, "/**/*.lock"),
		rule(ModeDeny, write, "/proj/*/secret"),
	}
	for _, tc := range []struct {
		target string
		recur  bool
		want   []string
	}{
		{"/data/file.txt", false, nil},
		{"/data/keep/x.txt", false, nil},
		{"/data", true, []string{"/data/keep/**", "/**/*.lock"}},
		{"/data/keep", true, []string{"/data/keep/**", "/**/*.lock"}},
		{"/proj/a/secret", false, []string{"/proj/*/secret"}},
		{"/proj/a", true, []string{"/**/*.lock", "/proj/*/secret"}},
		{"/proj/a/secret/inner", true, []string{"/**/*.lock", "/proj/*/secret"}},
		{"/proj", true, []string{"/**/*.lock", "/proj/*/secret"}},
	} {
		got := DeleteDenyPatterns(rules, tc.target, tc.recur)
		if !slices.Equal(got, tc.want) {
			t.Errorf("DeleteDenyPatterns(%s, %v) = %v, want %v", tc.target, tc.recur, got, tc.want)
		}
	}
	single := []FilesystemPermission{rule(ModeDeny, write, "/proj/*.key")}
	if got := DeleteDenyPatterns(single, "/proj/sub", true); got != nil {
		t.Errorf("a directory the single-component wildcard cannot reach must be deletable, got %v", got)
	}
	if got := DeleteDenyPatterns(single, "/proj/x.key/inner", true); len(got) != 1 {
		t.Errorf("a path under a denied directory must be blocked, got %v", got)
	}
	if got := DeleteDenyPatterns(single, "/elsewhere", true); got != nil {
		t.Errorf("unrelated subtree must be deletable, got %v", got)
	}
}

func TestInterruptAsDeny(t *testing.T) {
	in := []FilesystemPermission{rule(ModeInterrupt, write, "/x/**"), rule(ModeAllow, read, "/y")}
	out := InterruptAsDeny(in)
	if out[0].Mode != ModeDeny || out[1].Mode != ModeAllow || in[0].Mode != ModeInterrupt {
		t.Fatalf("InterruptAsDeny = %+v (input %+v)", out, in)
	}
}

func tc(name string, args map[string]any) llm.ToolCall {
	return llm.ToolCall{ID: "1", Name: name, Args: args}
}

func TestBuildInterruptOnNoInterruptRules(t *testing.T) {
	got, err := BuildInterruptOn([]FilesystemPermission{rule(ModeDeny, both, "/s/**")})
	if err != nil || got != nil {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestBuildInterruptOnInvalidRule(t *testing.T) {
	if _, err := BuildInterruptOn([]FilesystemPermission{rule(ModeInterrupt, read, "rel")}); err == nil {
		t.Fatal("invalid rule must fail")
	}
}

func TestBuildInterruptOnWriteRules(t *testing.T) {
	cfgs, err := BuildInterruptOn([]FilesystemPermission{
		rule(ModeAllow, write, "/config/local.yaml"),
		rule(ModeInterrupt, write, "/config/**"),
	})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for n := range cfgs {
		names = append(names, n)
	}
	slices.Sort(names)
	if want := []string{"delete", "edit_file", "write_file"}; !slices.Equal(names, want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}
	write := cfgs["write_file"].When
	for _, c := range []struct {
		args map[string]any
		want bool
	}{
		{map[string]any{"file_path": "/config/app.yaml"}, true},
		{map[string]any{"file_path": "/config/local.yaml"}, false},
		{map[string]any{"file_path": "/src/a.go"}, false},
		{map[string]any{"file_path": 3}, false},
	} {
		if got := write(tc("write_file", c.args)); got != c.want {
			t.Errorf("write_file When(%v) = %v", c.args, got)
		}
	}
	del := cfgs["delete"].When
	for _, c := range []struct {
		path string
		want bool
	}{
		{"/config", true},
		{"/", true},
		{"/config/local.yaml", true},
		{"/src", false},
	} {
		if got := del(tc("delete", map[string]any{"file_path": c.path})); got != c.want {
			t.Errorf("delete When(%s) = %v", c.path, got)
		}
	}
}

func TestBuildInterruptOnReadBulkTools(t *testing.T) {
	cfgs, err := BuildInterruptOn([]FilesystemPermission{rule(ModeInterrupt, read, "/private/**")})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		args map[string]any
		want bool
	}{
		{"ls", map[string]any{"path": "/private/a"}, true},
		{"ls", map[string]any{"path": "/public"}, false},
		{"grep", map[string]any{"pattern": "x"}, true},
		{"grep", map[string]any{"pattern": "x", "path": "/public"}, false},
		{"glob", map[string]any{"pattern": "/private/*.md", "path": "/public"}, true},
		{"glob", map[string]any{"pattern": "../private/*", "path": "/public"}, true},
		{"glob", map[string]any{"pattern": "*.md", "path": "/public"}, false},
		{"glob", map[string]any{"pattern": "*.md", "path": "."}, true},
		{"read_file", map[string]any{"file_path": "/private/k"}, true},
		{"read_file", map[string]any{"file_path": "/public/k"}, false},
	} {
		if got := cfgs[c.name].When(tc(c.name, c.args)); got != c.want {
			t.Errorf("%s When(%v) = %v, want %v", c.name, c.args, got, c.want)
		}
	}
	if _, ok := cfgs["write_file"]; ok {
		t.Fatal("read-only interrupt rules must not gate write_file")
	}
}
