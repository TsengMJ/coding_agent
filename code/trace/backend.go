package trace

import (
	"context"
	"strings"
	"time"

	udiff "github.com/aymanbagabas/go-udiff"

	"github.com/tsengmingchun/deepagents_golang/backend"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

// WrapBackend returns b with writes, edits and deletes made under a ctx
// carrying a recorder recorded as full unified diffs. The wrapper keeps
// b's optional capabilities (execution, delete).
func WrapBackend(b backend.Backend) backend.Backend {
	base := &diffBackend{Backend: b}
	sb, isSandbox := b.(backend.SandboxBackend)
	d, isDeleter := b.(backend.Deleter)
	switch {
	case isSandbox && isDeleter:
		return &sandboxDeleter{diffSandbox{base, sb}, diffDeleter{base, d}}
	case isSandbox:
		return &diffSandbox{base, sb}
	case isDeleter:
		return &struct {
			*diffBackend
			diffDeleter
		}{base, diffDeleter{base, d}}
	default:
		return base
	}
}

type diffBackend struct {
	backend.Backend
}

func (b *diffBackend) ArtifactsRoot() string { return backend.ArtifactsRoot(b.Backend) }

func (b *diffBackend) Write(ctx context.Context, path, content string) backend.WriteResult {
	sc := scopeFrom(ctx)
	if sc.rec == nil {
		return b.Backend.Write(ctx, path, content)
	}
	before, existed := b.content(ctx, path)
	res := b.Backend.Write(ctx, path, content)
	if res.Error == "" {
		after, _ := b.content(ctx, path)
		b.record(ctx, sc, path, "write", before, after, existed, true)
	}
	return res
}

func (b *diffBackend) Edit(ctx context.Context, path, oldString, newString string, replaceAll bool) backend.EditResult {
	sc := scopeFrom(ctx)
	if sc.rec == nil {
		return b.Backend.Edit(ctx, path, oldString, newString, replaceAll)
	}
	before, existed := b.content(ctx, path)
	res := b.Backend.Edit(ctx, path, oldString, newString, replaceAll)
	if res.Error == "" {
		after, _ := b.content(ctx, path)
		b.record(ctx, sc, path, "edit", before, after, existed, true)
	}
	return res
}

func (b *diffBackend) content(ctx context.Context, path string) (string, bool) {
	res := b.Backend.DownloadFiles(ctx, []string{path})
	if len(res) != 1 || res[0].Error != "" {
		return "", false
	}
	return string(res[0].Content), true
}

func (b *diffBackend) record(ctx context.Context, sc scope, path, op, before, after string, existed, exists bool) {
	from, to := path, path
	if !existed {
		from = "/dev/null"
	}
	if !exists {
		to = "/dev/null"
	}
	unified := udiff.Unified(from, to, before, after)
	added, removed := countChanges(unified)
	err := sc.rec.RecordDiff(Diff{
		ToolCallID: tool.CallIDFrom(ctx), Namespace: sc.ns, Path: path, Op: op,
		Unified: unified, Added: added, Removed: removed, Time: time.Now(),
	})
	if err != nil {
		sc.rec.warn(err)
	}
}

// countChanges counts added and removed lines of a unified diff.
func countChanges(unified string) (added, removed int) {
	inHunk := false
	for _, l := range strings.Split(unified, "\n") {
		switch {
		case strings.HasPrefix(l, "@@"):
			inHunk = true
		case !inHunk:
		case strings.HasPrefix(l, "+"):
			added++
		case strings.HasPrefix(l, "-"):
			removed++
		}
	}
	return added, removed
}

type diffSandbox struct {
	*diffBackend
	sb backend.SandboxBackend
}

func (s *diffSandbox) ID() string { return s.sb.ID() }

func (s *diffSandbox) Execute(ctx context.Context, command string, timeout *time.Duration) backend.ExecuteResponse {
	return s.sb.Execute(ctx, command, timeout)
}

type diffDeleter struct {
	b *diffBackend
	d backend.Deleter
}

func (d diffDeleter) Delete(ctx context.Context, path string) backend.DeleteResult {
	sc := scopeFrom(ctx)
	if sc.rec == nil {
		return d.d.Delete(ctx, path)
	}
	before, existed := d.b.content(ctx, path)
	res := d.d.Delete(ctx, path)
	if res.Error == "" && existed {
		d.b.record(ctx, sc, path, "delete", before, "", true, false)
	}
	return res
}

type sandboxDeleter struct {
	diffSandbox
	diffDeleter
}
