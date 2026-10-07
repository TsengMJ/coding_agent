package codetools

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/tsengmingchun/quill/backend"
)

// maxRelated caps each list FindRelated returns.
const maxRelated = 20

// Related is what verifying a change to some files should run, from the
// narrowest to the widest: the tests of the changed code, then the tests of
// code that depends on it. Paths are relative to Dir.
type Related struct {
	Language string `json:"language"`
	// Dir is the project directory relative to the workspace root ("" = root).
	Dir string `json:"dir,omitempty"`
	// Packages are the Go packages of the changed files ("./a").
	Packages []string `json:"packages,omitempty"`
	// Dependents are the Go packages that import them, tests included.
	Dependents []string `json:"dependents,omitempty"`
	// Tests are the Node / Python test files named after the changed files,
	// and the changed test files themselves.
	Tests []string `json:"tests,omitempty"`
	// Importers are other Node / Python test files that import a changed
	// module.
	Importers []string `json:"importers,omitempty"`
}

// testFiles recognizes a language's test files, in Go and in `grep -E`
// syntax for the find output ("./dir/file").
type testFiles struct {
	re   *regexp.Regexp
	grep string
}

var (
	nodeTests = testFiles{
		re:   regexp.MustCompile(`(?:\.(?:test|spec)\.[cm]?[jt]sx?$|(?:^|/)(?:test|tests|__tests__)/.*\.[cm]?[jt]sx?$)`),
		grep: `\.(test|spec)\.[cm]?[jt]sx?$|/(test|tests|__tests__)/.*\.[cm]?[jt]sx?$`,
	}
	pyTests = testFiles{
		re:   regexp.MustCompile(`(?:^|/)(?:test_[^/]*|[^/]*_test)\.py$`),
		grep: `/(test_[^/]*|[^/]*_test)\.py$`,
	}
	moduleName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// FindRelated finds the tests to run for a change to files (paths relative
// to the workspace root): Go packages and their importers through
// `go list`, Node and Python test files by name and by import.
func FindRelated(ctx context.Context, sb backend.SandboxBackend, files []string, timeout time.Duration) (Related, error) {
	if len(files) == 0 {
		return Related{}, nil
	}
	t, res := resolveTarget(ctx, sb, map[string]any{"files": toAny(files)})
	if res != nil {
		return Related{}, resultError(res)
	}
	r := Related{Language: t.lang, Dir: t.dir}
	tsb := t.sandbox(sb)
	var err error
	switch t.lang {
	case langGo:
		err = goRelated(ctx, tsb, t.files, timeout, &r)
	case langNode:
		err = scriptRelated(ctx, tsb, t.files, timeout, &r, nodeTests, nodeFindTests, nodeImportPattern)
	case langPython:
		err = scriptRelated(ctx, tsb, t.files, timeout, &r, pyTests, pyFindTests, pyImportPattern)
	default:
		err = fmt.Errorf("no related-test search for %s", strings.Join(files, ", "))
	}
	return r, err
}

func goRelated(ctx context.Context, sb backend.SandboxBackend, files []string, timeout time.Duration, r *Related) error {
	for _, f := range files {
		if strings.HasSuffix(f, ".go") {
			if p := goPackageOf(f); !slices.Contains(r.Packages, p) {
				r.Packages = append(r.Packages, p)
			}
		}
	}
	if len(r.Packages) == 0 {
		return nil
	}
	format := `{{.ImportPath}}{{"\t"}}{{.Dir}}{{"\t"}}{{join .Imports " "}} {{join .TestImports " "}} {{join .XTestImports " "}}`
	cmd := goModuleCheck + `dirname "$(go env GOMOD)"; go list -e -f ` + shellQuote(format) + ` ./... 2>/dev/null; echo ` + exitMarker + "$?"
	res := run(ctx, sb, cmd, timeout)
	if u := unsupported(res.Stdout); u != nil {
		return resultError(u)
	}
	outs, codes := splitExit(res.Stdout)
	if len(codes) == 0 {
		return fmt.Errorf("go list did not finish: %s", clipRaw(res.errText()))
	}
	lines := strings.Split(strings.TrimSpace(outs[0]), "\n")
	root := strings.TrimSpace(lines[0])
	type pkg struct{ rel, imports string }
	var pkgs []pkg
	changed := map[string]bool{}
	for _, l := range lines[1:] {
		parts := strings.SplitN(l, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		rel := "."
		if d := strings.TrimPrefix(parts[1], root+"/"); d != parts[1] {
			rel = "./" + d
		} else if parts[1] != root {
			continue
		}
		if slices.Contains(r.Packages, rel) {
			changed[parts[0]] = true
			continue
		}
		pkgs = append(pkgs, pkg{rel, " " + parts[2] + " "})
	}
	for _, p := range pkgs {
		for imp := range changed {
			if strings.Contains(p.imports, " "+imp+" ") && !slices.Contains(r.Dependents, p.rel) {
				r.Dependents = append(r.Dependents, p.rel)
			}
		}
	}
	slices.Sort(r.Dependents)
	r.Dependents = r.Dependents[:min(len(r.Dependents), maxRelated)]
	return nil
}

func goPackageOf(file string) string {
	if d := path.Dir(file); d != "." {
		return "./" + d
	}
	return "."
}

const (
	nodeFindTests = `find . \( -name node_modules -o -name .git -o -name dist -o -name build -o -name coverage -o -name .next \) -prune -o -type f \( -name '*.js' -o -name '*.cjs' -o -name '*.mjs' -o -name '*.jsx' -o -name '*.ts' -o -name '*.tsx' \) -print 2>/dev/null`
	pyFindTests   = `find . \( -name .git -o -name node_modules -o -name venv -o -name .venv -o -name env -o -name __pycache__ -o -name .tox -o -name build -o -name dist -o -name site-packages \) -prune -o -type f -name '*.py' -print 2>/dev/null`
)

// nodeImportPattern matches a require / import of a module whose file name
// is one of names.
func nodeImportPattern(names []string) string {
	return `(require\(|from |import\()[[:space:]]*['"][^'"]*/(` + strings.Join(names, "|") + `)(\.[cm]?[jt]sx?)?['"]`
}

// pyImportPattern matches "import a.b.name", "from a.name import x" and
// "from a import name".
func pyImportPattern(names []string) string {
	alt := "(" + strings.Join(names, "|") + ")"
	return `^[[:space:]]*((from|import)[[:space:]]+([A-Za-z0-9_.]*\.)?` + alt + `([^A-Za-z0-9_]|$)|from[[:space:]]+[A-Za-z0-9_.]+[[:space:]]+import[[:space:]].*(^|[^A-Za-z0-9_])` + alt + `([^A-Za-z0-9_]|$))`
}

// scriptRelated lists the project's test files, picks the ones named after
// a changed file, and greps the others for an import of a changed module.
func scriptRelated(ctx context.Context, sb backend.SandboxBackend, files []string, timeout time.Duration, r *Related,
	tests testFiles, find string, importPattern func([]string) string) error {
	var names []string
	for _, f := range files {
		if tests.re.MatchString(f) {
			r.Tests = append(r.Tests, f)
			continue
		}
		base := strings.TrimSuffix(path.Base(f), path.Ext(f))
		if moduleName.MatchString(base) && !slices.Contains(names, base) {
			names = append(names, base)
		}
	}
	if len(names) == 0 {
		return nil
	}
	cmd := find + "; echo " + exitMarker + "$?; " +
		find + " | grep -E " + shellQuote(tests.grep) + " | tr '\\n' '\\0' | xargs -0 grep -lE " + shellQuote(importPattern(names)) + " 2>/dev/null; echo " + exitMarker + "0"
	res := run(ctx, sb, cmd, timeout)
	outs, codes := splitExit(res.Stdout)
	if len(codes) < 2 {
		return fmt.Errorf("test search did not finish: %s", clipRaw(res.errText()))
	}
	imports := map[string]bool{}
	for _, l := range strings.Split(outs[1], "\n") {
		if l = strings.TrimPrefix(strings.TrimSpace(l), "./"); l != "" {
			imports[l] = true
		}
	}
	for _, l := range strings.Split(outs[0], "\n") {
		l = strings.TrimPrefix(strings.TrimSpace(l), "./")
		if l == "" || !tests.re.MatchString(l) || slices.Contains(r.Tests, l) {
			continue
		}
		switch {
		case namedAfter(l, names):
			r.Tests = append(r.Tests, l)
		case imports[l]:
			r.Importers = append(r.Importers, l)
		}
	}
	slices.Sort(r.Tests)
	slices.Sort(r.Importers)
	r.Tests = r.Tests[:min(len(r.Tests), maxRelated)]
	r.Importers = r.Importers[:min(len(r.Importers), maxRelated)]
	return nil
}

// namedAfter reports a test file named after one of the modules:
// user.test.js, user.spec.ts, test_user.py, user_test.py, or user.js in a
// test directory.
func namedAfter(file string, names []string) bool {
	base := path.Base(file)
	stem := strings.TrimSuffix(base, path.Ext(base))
	for _, n := range names {
		if strings.HasPrefix(base, n+".test.") || strings.HasPrefix(base, n+".spec.") ||
			stem == "test_"+n || stem == n+"_test" || stem == n {
			return true
		}
	}
	return false
}
