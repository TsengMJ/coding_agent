package codetools

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/tsengmingchun/quill/apps/agent/backend"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

const (
	maxSearchDepth = 3
	maxCandidates  = 10
)

// searchPrune lists directories the shallow project search skips.
var searchPrune = []string{"node_modules", ".git", "venv", ".venv", "vendor", "dist", "build", "__pycache__", ".tox"}

// target is where run_tests or diagnostics run: the project root, or the
// subdirectory that holds the project (monorepos).
type target struct {
	p     project
	lang  string
	dir   string   // project-relative; "" is the root
	files []string // relative to dir
}

// sandbox runs commands inside the target directory.
func (t target) sandbox(sb backend.SandboxBackend) backend.SandboxBackend {
	if t.dir == "" {
		return sb
	}
	return dirSandbox{SandboxBackend: sb, dir: t.dir}
}

// rootPath turns a path reported relative to the target directory back into
// a project-relative one.
func (t target) rootPath(p string) string {
	if t.dir == "" || p == "" || path.IsAbs(p) {
		return p
	}
	return path.Join(t.dir, p)
}

// fixFailures makes failure paths project-relative; a Package that is the
// test file (Node) moves with it.
func (t target) fixFailures(rep *TestReport) {
	for i := range rep.Failures {
		f := &rep.Failures[i]
		if f.Package == f.File {
			f.Package = t.rootPath(f.Package)
		}
		f.File = t.rootPath(f.File)
	}
}

// dirSandbox runs every command after cd-ing into dir.
type dirSandbox struct {
	backend.SandboxBackend
	dir string
}

func (d dirSandbox) Execute(ctx context.Context, command string, timeout *time.Duration) backend.ExecuteResponse {
	return d.SandboxBackend.Execute(ctx, "cd "+shellQuote(d.dir)+" || exit 1\n"+command, timeout)
}

// resolveTarget picks the directory and language for a run_tests or
// diagnostics call from its "dir", "language" and "files" arguments. When
// the root has no project files for the language, it looks for the project
// above the given files, then in a shallow search; several candidates
// return an Unsupported result listing them.
func resolveTarget(ctx context.Context, sb backend.SandboxBackend, args map[string]any) (target, *tool.Result) {
	explicit, err := stringArg(args, "language")
	if err != nil {
		return target{}, errorResult("Error: %v", err)
	}
	dir, err := stringArg(args, "dir")
	if err != nil {
		return target{}, errorResult("Error: %v", err)
	}
	if dir != "" {
		if dir, err = projectPath(dir); err != nil {
			return target{}, errorResult("Error: %v", err)
		}
		if dir == "." {
			dir = ""
		}
	}
	files, err := projectPaths(args, "files")
	if err != nil {
		return target{}, errorResult("Error: %v", err)
	}
	t := target{dir: dir}
	t.p = probeProject(ctx, t.sandbox(sb))
	lang, err := t.p.language(explicit, files)
	if err != nil {
		return target{}, errorResult("Error: %v", err)
	}
	if dir == "" && !t.p.hasAny(languageMarkers[lang]) {
		found, candidates := locateProject(ctx, sb, files, lang)
		switch {
		case found != "":
			t.dir = found
			t.p = probeProject(ctx, t.sandbox(sb))
			if lang == "" {
				lang, _ = t.p.language("", nil)
			}
		case len(candidates) > 1:
			return target{}, jsonResult(Unsupported{
				Status: "unsupported",
				Reason: "no project files at the project root, and several projects below it: " + strings.Join(candidates, ", "),
				Hint:   fmt.Sprintf("pass dir (e.g. %q) or files inside one project", candidates[0]),
			})
		}
	}
	t.lang = lang
	if t.files, err = relativeTo(files, t.dir); err != nil {
		return target{}, errorResult("Error: %v", err)
	}
	return t, nil
}

// locateProject returns the project directory above the first file that has
// one, else the only directory within maxSearchDepth holding project files
// for lang. With several matches it returns them as candidates instead.
func locateProject(ctx context.Context, sb backend.SandboxBackend, files []string, lang string) (string, []string) {
	markers := languageMarkers[lang]
	if len(files) > 0 {
		var dirs []string
		for _, f := range files {
			for d := path.Dir(f); d != "." && d != "/"; d = path.Dir(d) {
				dirs = append(dirs, d)
			}
		}
		if len(dirs) == 0 {
			return "", nil
		}
		script := fmt.Sprintf(`for d in %s; do for f in %s; do if [ -e "$d/$f" ]; then echo "$d"; break; fi; done; done`,
			quoteAll(dirs), strings.Join(markers, " "))
		found := map[string]bool{}
		for _, l := range strings.Split(run(ctx, sb, script, probeTimeout).Stdout, "\n") {
			found[strings.TrimSpace(l)] = true
		}
		for _, d := range dirs {
			if found[d] {
				return d, nil
			}
		}
		return "", nil
	}
	prune := make([]string, len(searchPrune))
	for i, n := range searchPrune {
		prune[i] = "-name " + shellQuote(n)
	}
	names := make([]string, len(markers))
	for i, n := range markers {
		names[i] = "-name " + shellQuote(n)
	}
	script := fmt.Sprintf(`find . -maxdepth %d \( %s \) -prune -o -type f \( %s \) -print 2>/dev/null`,
		maxSearchDepth+1, strings.Join(prune, " -o "), strings.Join(names, " -o "))
	var candidates []string
	for _, l := range strings.Split(run(ctx, sb, script, probeTimeout).Stdout, "\n") {
		if l = strings.TrimSpace(l); l == "" {
			continue
		}
		d := strings.TrimPrefix(path.Dir(l), "./")
		if d != "." && !slices.Contains(candidates, d) {
			candidates = append(candidates, d)
		}
	}
	slices.Sort(candidates)
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	if len(candidates) > maxCandidates {
		candidates = candidates[:maxCandidates]
	}
	return "", candidates
}

// relativeTo rewrites project-relative files relative to dir.
func relativeTo(files []string, dir string) ([]string, error) {
	if dir == "" {
		return files, nil
	}
	out := make([]string, len(files))
	for i, f := range files {
		switch {
		case f == dir:
			out[i] = "."
		case strings.HasPrefix(f, dir+"/"):
			out[i] = strings.TrimPrefix(f, dir+"/")
		default:
			return nil, fmt.Errorf("file %q is outside the project directory %q; pass files from one project at a time", f, dir)
		}
	}
	return out, nil
}
