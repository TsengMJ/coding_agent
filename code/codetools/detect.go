package codetools

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/tsengmingchun/deepagents_golang/backend"
)

// Project languages run_tests and diagnostics understand.
const (
	langGo     = "go"
	langNode   = "node"
	langPython = "python"
)

// Node test frameworks; frameworkNpm means "run npm test and report the raw output".
const (
	frameworkMocha    = "mocha"
	frameworkJest     = "jest"
	frameworkVitest   = "vitest"
	frameworkNpm      = "npm"
	frameworkPytest   = "pytest"
	frameworkUnittest = "unittest"
	frameworkGo       = "go test"
)

const (
	probeTimeout  = 30 * time.Second
	packageMarker = "__DCODE_PKG__"
)

// markerFiles are looked up at the project root.
var markerFiles = []string{
	"go.mod", "go.work", "package.json", "tsconfig.json",
	"pyproject.toml", "pytest.ini", "setup.cfg", "setup.py", "tox.ini",
	".eslintrc", ".eslintrc.js", ".eslintrc.cjs", ".eslintrc.json", ".eslintrc.yml", ".eslintrc.yaml",
	"eslint.config.js", "eslint.config.mjs", "eslint.config.cjs", "eslint.config.ts",
}

var (
	pythonMarkers = []string{"pyproject.toml", "pytest.ini", "setup.cfg", "setup.py", "tox.ini"}
	probeCommands = []string{"go", "node", "npx", "npm", "python3", "python", "ruff"}
	// probeScript prints "file:NAME" and "cmd:NAME" lines, then package.json.
	probeScript = fmt.Sprintf(`for f in %s; do [ -e "$f" ] && echo "file:$f"; done; `+
		`for c in %s; do command -v "$c" >/dev/null 2>&1 && echo "cmd:$c"; done; `+
		`if [ -f package.json ]; then echo %s; head -c 262144 package.json; fi; true`,
		strings.Join(markerFiles, " "), strings.Join(probeCommands, " "), packageMarker)
)

// packageJSON holds the package.json fields framework detection reads.
type packageJSON struct {
	Scripts         map[string]string `json:"scripts"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
	ESLintConfig    json.RawMessage   `json:"eslintConfig"`
}

// project is what the probe found at the project root.
type project struct {
	files map[string]bool
	cmds  map[string]bool
	pkg   packageJSON
}

func probeProject(ctx context.Context, sb backend.SandboxBackend) project {
	return parseProbe(run(ctx, sb, probeScript, probeTimeout).Stdout)
}

func parseProbe(out string) project {
	p := project{files: map[string]bool{}, cmds: map[string]bool{}}
	head, pkg, hasPkg := strings.Cut(out, packageMarker+"\n")
	for _, l := range strings.Split(head, "\n") {
		if name, ok := strings.CutPrefix(l, "file:"); ok {
			p.files[strings.TrimSpace(name)] = true
		} else if name, ok := strings.CutPrefix(l, "cmd:"); ok {
			p.cmds[strings.TrimSpace(name)] = true
		}
	}
	if hasPkg {
		_ = json.Unmarshal([]byte(pkg), &p.pkg)
	}
	return p
}

// python returns the Python interpreter to run, or "".
func (p project) python() string {
	switch {
	case p.cmds["python3"]:
		return "python3"
	case p.cmds["python"]:
		return "python"
	}
	return ""
}

func (p project) hasESLintConfig() bool {
	for f := range p.files {
		if strings.HasPrefix(f, ".eslintrc") || strings.HasPrefix(f, "eslint.config.") {
			return true
		}
	}
	return len(p.pkg.ESLintConfig) > 0
}

// nodeFramework picks the test runner from the test script, then from the
// dependencies.
func (p project) nodeFramework() string {
	order := []string{frameworkVitest, frameworkJest, frameworkMocha}
	script := p.pkg.Scripts["test"]
	for _, f := range order {
		if strings.Contains(script, f) {
			return f
		}
	}
	for _, f := range order {
		if _, ok := p.pkg.DevDependencies[f]; ok {
			return f
		}
		if _, ok := p.pkg.Dependencies[f]; ok {
			return f
		}
	}
	return frameworkNpm
}

var extLanguage = map[string]string{
	".go": langGo, ".py": langPython,
	".js": langNode, ".cjs": langNode, ".mjs": langNode, ".jsx": langNode,
	".ts": langNode, ".cts": langNode, ".mts": langNode, ".tsx": langNode,
}

var languageAliases = map[string]string{
	"go": langGo, "golang": langGo,
	"node": langNode, "javascript": langNode, "js": langNode, "typescript": langNode, "ts": langNode,
	"python": langPython, "py": langPython,
}

// language picks the project language: the explicit argument, then the
// extensions of the given files, then marker files (Go, Python, Node; a
// Python project often ships a package.json for its front end). It returns
// "" when nothing matches.
func (p project) language(explicit string, files []string) (string, error) {
	if explicit != "" {
		lang, ok := languageAliases[strings.ToLower(explicit)]
		if !ok {
			return "", fmt.Errorf("unknown language %q: use go, node (JavaScript/TypeScript) or python", explicit)
		}
		return lang, nil
	}
	for _, f := range files {
		if lang, ok := extLanguage[strings.ToLower(path.Ext(f))]; ok {
			return lang, nil
		}
	}
	switch {
	case p.files["go.mod"] || p.files["go.work"]:
		return langGo, nil
	case p.hasAny(pythonMarkers):
		return langPython, nil
	case p.files["package.json"]:
		return langNode, nil
	}
	return "", nil
}

// languageMarkers are the files that make a directory a project of each
// language; "" means any language.
var languageMarkers = map[string][]string{
	langGo:     {"go.mod", "go.work"},
	langNode:   {"package.json"},
	langPython: pythonMarkers,
	"":         append([]string{"go.mod", "go.work", "package.json"}, pythonMarkers...),
}

func (p project) hasAny(names []string) bool {
	for _, n := range names {
		if p.files[n] {
			return true
		}
	}
	return false
}
