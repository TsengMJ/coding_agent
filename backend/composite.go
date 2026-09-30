package backend

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// CompositeBackend routes paths to backends by longest matching prefix and
// merges ls/grep/glob results across routes (Python `CompositeBackend`).
// A routed backend sees paths with the route prefix stripped ("/memories/x"
// becomes "/x"); result paths are rewritten back to the caller's view.
type CompositeBackend struct {
	defaultBackend Backend
	// routes is sorted longest prefix first; every prefix ends with "/".
	routes []compositeRoute
	// artifactsRoot is where offloaded artifacts (messages, outputs) are written.
	artifactsRoot string
}

type compositeRoute struct {
	prefix  string
	backend Backend
}

// NewCompositeBackend returns a router over routes, falling back to def.
// Route prefixes must start with "/"; a missing trailing "/" is added. The
// result executes commands and deletes files only when def does: Execute
// always runs on def, and Delete fails for routes whose backend cannot delete.
func NewCompositeBackend(def Backend, routes map[string]Backend, artifactsRoot string) Backend {
	c := newComposite(def, routes, artifactsRoot)
	sb, isSandbox := def.(SandboxBackend)
	_, isDeleter := def.(Deleter)
	switch {
	case isSandbox && isDeleter:
		return &struct {
			compositeSandbox
			compositeDeleter
		}{compositeSandbox{c, sb}, compositeDeleter{c}}
	case isSandbox:
		return &compositeSandbox{c, sb}
	case isDeleter:
		return &struct {
			*CompositeBackend
			compositeDeleter
		}{c, compositeDeleter{c}}
	default:
		return c
	}
}

func newComposite(def Backend, routes map[string]Backend, artifactsRoot string) *CompositeBackend {
	if artifactsRoot == "" {
		artifactsRoot = "/"
	}
	c := &CompositeBackend{defaultBackend: def, artifactsRoot: artifactsRoot}
	for p, b := range routes {
		if !strings.HasSuffix(p, "/") {
			p += "/"
		}
		c.routes = append(c.routes, compositeRoute{prefix: p, backend: b})
	}
	slices.SortFunc(c.routes, func(a, b compositeRoute) int {
		if n := cmp.Compare(len(b.prefix), len(a.prefix)); n != 0 {
			return n
		}
		return strings.Compare(a.prefix, b.prefix)
	})
	return c
}

// ArtifactsRoot is where middleware offloads large tool results and messages.
func (c *CompositeBackend) ArtifactsRoot() string { return c.artifactsRoot }

// Default returns the backend for paths that match no route.
func (c *CompositeBackend) Default() Backend { return c.defaultBackend }

// route returns the backend for p, the path to hand it, and the matched
// prefix ("" for the default backend).
func (c *CompositeBackend) route(p string) (Backend, string, string) {
	for _, r := range c.routes {
		if p == strings.TrimSuffix(r.prefix, "/") {
			return r.backend, "/", r.prefix
		}
		if rest, ok := strings.CutPrefix(p, r.prefix); ok {
			return r.backend, "/" + rest, r.prefix
		}
	}
	return c.defaultBackend, p, ""
}

func remapPath(prefix, p string) string {
	if prefix == "" || p == "" {
		return p
	}
	return strings.TrimSuffix(prefix, "/") + p
}

func (c *CompositeBackend) Ls(ctx context.Context, p string) LsResult {
	b, bp, prefix := c.route(p)
	if prefix != "" {
		res := b.Ls(ctx, bp)
		if res.Error != "" {
			return res
		}
		return LsResult{Entries: remapInfos(prefix, res.Entries)}
	}
	res := c.defaultBackend.Ls(ctx, p)
	if p != "/" || res.Error != "" {
		return res
	}
	entries := slices.Clone(res.Entries)
	for _, r := range c.routes {
		entries = append(entries, FileInfo{Path: r.prefix, IsDir: true})
	}
	slices.SortFunc(entries, func(a, b FileInfo) int { return strings.Compare(a.Path, b.Path) })
	return LsResult{Entries: entries}
}

func remapInfos(prefix string, in []FileInfo) []FileInfo {
	out := make([]FileInfo, len(in))
	for i, fi := range in {
		fi.Path = remapPath(prefix, fi.Path)
		out[i] = fi
	}
	return out
}

func remapMatches(prefix string, in []GrepMatch) []GrepMatch {
	out := make([]GrepMatch, len(in))
	for i, m := range in {
		m.Path = remapPath(prefix, m.Path)
		out[i] = m
	}
	return out
}

func (c *CompositeBackend) Read(ctx context.Context, p string, offset, limit int) ReadResult {
	b, bp, _ := c.route(p)
	return b.Read(ctx, bp, offset, limit)
}

func (c *CompositeBackend) Write(ctx context.Context, p, content string) WriteResult {
	b, bp, prefix := c.route(p)
	res := b.Write(ctx, bp, content)
	if prefix != "" && res.Path != "" {
		res.Path = p
	}
	return res
}

func (c *CompositeBackend) Edit(ctx context.Context, p, oldString, newString string, replaceAll bool) EditResult {
	b, bp, prefix := c.route(p)
	res := b.Edit(ctx, bp, oldString, newString, replaceAll)
	if prefix != "" && res.Path != "" {
		res.Path = p
	}
	return res
}

// Grep searches one route when opts.Path is inside it, every backend when
// opts.Path is "" or "/", and otherwise only the default backend. MaxCount
// is a budget across all backends; once spent the remaining routes are
// skipped and the result is marked truncated.
func (c *CompositeBackend) Grep(ctx context.Context, pattern string, opts GrepOptions) GrepResult {
	if opts.Path != "" {
		if b, bp, prefix := c.route(opts.Path); prefix != "" {
			sub := opts
			sub.Path = bp
			res := capGrep(b.Grep(ctx, pattern, sub), opts.MaxCount)
			if res.Error != "" {
				return res
			}
			return GrepResult{Matches: remapMatches(prefix, res.Matches), Truncated: res.Truncated}
		}
	}
	if opts.Path != "" && opts.Path != "/" {
		return c.defaultBackend.Grep(ctx, pattern, opts)
	}
	res := capGrep(c.defaultBackend.Grep(ctx, pattern, opts), opts.MaxCount)
	if res.Error != "" {
		return res
	}
	out := GrepResult{Matches: slices.Clone(res.Matches), Truncated: res.Truncated}
	for _, r := range c.routes {
		remaining := 0
		if opts.MaxCount > 0 {
			remaining = opts.MaxCount - len(out.Matches)
			if remaining <= 0 {
				out.Truncated = true
				break
			}
		}
		sub := opts
		sub.Path, sub.MaxCount = "/", remaining
		res := capGrep(r.backend.Grep(ctx, pattern, sub), remaining)
		if res.Error != "" {
			return res
		}
		out.Matches = append(out.Matches, remapMatches(r.prefix, res.Matches)...)
		out.Truncated = out.Truncated || res.Truncated
	}
	return out
}

// capGrep enforces max on backends that ignore GrepOptions.MaxCount.
func capGrep(res GrepResult, max int) GrepResult {
	if max > 0 && len(res.Matches) > max {
		res.Matches = res.Matches[:max]
		res.Truncated = true
	}
	return res
}

// Glob searches one route when path is inside it, every backend when path
// is "" or "/", and otherwise only the default backend. When merging, a
// pattern that targets a route is rewritten relative to it, a bare pattern
// is forwarded unchanged, and a root-anchored pattern that targets no route
// skips the routed backends.
func (c *CompositeBackend) Glob(ctx context.Context, pattern, p string) GlobResult {
	if p != "" {
		if b, bp, prefix := c.route(p); prefix != "" {
			res := b.Glob(ctx, pattern, bp)
			if res.Error != "" {
				return res
			}
			res.Matches = remapInfos(prefix, res.Matches)
			return res
		}
	}
	if p != "" && p != "/" {
		return c.defaultBackend.Glob(ctx, pattern, p)
	}
	res := c.defaultBackend.Glob(ctx, pattern, p)
	if res.Error != "" {
		return res
	}
	out := GlobResult{Matches: slices.Clone(res.Matches), Truncated: res.Truncated, TruncationReason: res.TruncationReason}
	for _, r := range c.routes {
		rp, ok := routeGlobPattern(pattern, r.prefix)
		if !ok {
			continue
		}
		sub := r.backend.Glob(ctx, rp, "/")
		if sub.Error != "" {
			return sub
		}
		out.Matches = append(out.Matches, remapInfos(r.prefix, sub.Matches)...)
		out.Truncated = out.Truncated || sub.Truncated
		out.TruncationReason = mergeTruncationReason(out.TruncationReason, sub.TruncationReason)
	}
	slices.SortFunc(out.Matches, func(a, b FileInfo) int { return strings.Compare(a.Path, b.Path) })
	return out
}

// routeGlobPattern adapts pattern for the backend at prefix; false skips it.
func routeGlobPattern(pattern, prefix string) (string, bool) {
	bare := strings.TrimLeft(pattern, "/")
	barePrefix := strings.Trim(prefix, "/") + "/"
	if rest, ok := strings.CutPrefix(bare, barePrefix); ok {
		return "/" + rest, true
	}
	if strings.HasPrefix(pattern, "/") {
		return "", false
	}
	return pattern, true
}

// mergeTruncationReason prefers "unreadable": narrowing the search cannot fix it.
func mergeTruncationReason(cur, in GlobTruncationReason) GlobTruncationReason {
	if cur == GlobTruncatedUnreadable || in == GlobTruncatedUnreadable {
		return GlobTruncatedUnreadable
	}
	return cmp.Or(cur, in)
}

// UploadFiles batches files per backend and returns responses in input order.
func (c *CompositeBackend) UploadFiles(ctx context.Context, files []FileUpload) []FileUploadResponse {
	out := make([]FileUploadResponse, len(files))
	groups, order := map[Backend][]int{}, []Backend{}
	routed := make([]FileUpload, len(files))
	for i, f := range files {
		b, bp, _ := c.route(f.Path)
		if _, ok := groups[b]; !ok {
			order = append(order, b)
		}
		groups[b] = append(groups[b], i)
		routed[i] = FileUpload{Path: bp, Content: f.Content}
	}
	for _, b := range order {
		idx := groups[b]
		batch := make([]FileUpload, len(idx))
		for j, i := range idx {
			batch[j] = routed[i]
		}
		res := b.UploadFiles(ctx, batch)
		for j, i := range idx {
			out[i] = FileUploadResponse{Path: files[i].Path}
			if j < len(res) {
				out[i].Error = res[j].Error
			}
		}
	}
	return out
}

// DownloadFiles batches paths per backend and returns responses in input order.
func (c *CompositeBackend) DownloadFiles(ctx context.Context, paths []string) []FileDownloadResponse {
	out := make([]FileDownloadResponse, len(paths))
	groups, order := map[Backend][]int{}, []Backend{}
	routed := make([]string, len(paths))
	for i, p := range paths {
		b, bp, _ := c.route(p)
		if _, ok := groups[b]; !ok {
			order = append(order, b)
		}
		groups[b] = append(groups[b], i)
		routed[i] = bp
	}
	for _, b := range order {
		idx := groups[b]
		batch := make([]string, len(idx))
		for j, i := range idx {
			batch[j] = routed[i]
		}
		res := b.DownloadFiles(ctx, batch)
		for j, i := range idx {
			out[i] = FileDownloadResponse{Path: paths[i]}
			if j < len(res) {
				out[i].Content, out[i].Error = res[j].Content, res[j].Error
			}
		}
	}
	return out
}

type compositeSandbox struct {
	*CompositeBackend
	sb SandboxBackend
}

func (s *compositeSandbox) ID() string { return s.sb.ID() }

func (s *compositeSandbox) Execute(ctx context.Context, command string, timeout *time.Duration) ExecuteResponse {
	return s.sb.Execute(ctx, command, timeout)
}

type compositeDeleter struct{ c *CompositeBackend }

func (d compositeDeleter) Delete(ctx context.Context, p string) DeleteResult {
	b, bp, prefix := d.c.route(p)
	del, ok := b.(Deleter)
	if !ok {
		return DeleteResult{Error: fmt.Sprintf("Error: deletion is not supported for '%s'.", p)}
	}
	res := del.Delete(ctx, bp)
	if prefix != "" && res.Path != "" {
		res.Path = p
	}
	return res
}
