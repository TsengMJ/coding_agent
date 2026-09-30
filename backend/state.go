package backend

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/tsengmingchun/deepagents_golang/runtime"
)

// FilesStateKey is the graph-state key holding StateBackend files
// (map[string]FileData; a nil value deletes the path).
const FilesStateKey = "files"

// errNoRunContext explains how to use StateBackend outside a graph run.
var errNoRunContext = errors.New("StateBackend must be used inside an agent run (e.g. via deepagents.CreateDeepAgent); " +
	`to pre-populate files, pass them on invoke: agent.Input{Values: map[string]any{"files": ...}}`)

// StateBackend stores files in the agent graph state under FilesStateKey.
// Files are thread-scoped and checkpointed with the conversation.
// It requires a RunContext in ctx (see WithRunContext); writes are queued in
// RunContext.Pending and applied by the caller at the node boundary.
type StateBackend struct{}

// NewStateBackend returns a state-backed file store.
func NewStateBackend() *StateBackend { return &StateBackend{} }

// NewRunContext returns a RunContext reading state and collecting writes.
func NewRunContext(state *runtime.State) *RunContext {
	return &RunContext{State: state, Pending: &runtime.Update{}}
}

// FilesUpdate returns the files update queued by backend writes, or nil.
func (rc *RunContext) FilesUpdate() map[string]*FileData {
	if rc == nil || rc.Pending == nil {
		return nil
	}
	upd, _ := rc.Pending.Values[FilesStateKey].(map[string]*FileData)
	if len(upd) == 0 {
		return nil
	}
	return upd
}

// files returns the state files with this run's pending writes applied.
func (b *StateBackend) files(ctx context.Context) (map[string]FileData, *RunContext, error) {
	rc := RunContextFrom(ctx)
	if rc == nil {
		return nil, nil, errNoRunContext
	}
	var current any
	if rc.State != nil {
		current = rc.State.Values[FilesStateKey]
	}
	files, err := FilesReducer(current, rc.FilesUpdate())
	if err != nil {
		return nil, nil, err
	}
	return files.(map[string]FileData), rc, nil
}

// send queues a partial files update; nil values delete paths.
func (b *StateBackend) send(rc *RunContext, upd map[string]*FileData) {
	if rc.Pending == nil {
		rc.Pending = &runtime.Update{}
	}
	if rc.Pending.Values == nil {
		rc.Pending.Values = map[string]any{}
	}
	pending, _ := rc.Pending.Values[FilesStateKey].(map[string]*FileData)
	if pending == nil {
		pending = map[string]*FileData{}
	}
	maps.Copy(pending, upd)
	rc.Pending.Values[FilesStateKey] = pending
}

func (b *StateBackend) Ls(ctx context.Context, path string) LsResult {
	files, _, err := b.files(ctx)
	if err != nil {
		return LsResult{Error: err.Error()}
	}
	dir := path
	if !strings.HasSuffix(dir, "/") {
		dir += "/"
	}
	var entries []FileInfo
	subdirs := map[string]bool{}
	for p, fd := range files {
		rel, ok := strings.CutPrefix(p, dir)
		if !ok {
			continue
		}
		if name, _, nested := strings.Cut(rel, "/"); nested {
			subdirs[dir+name+"/"] = true
			continue
		}
		entries = append(entries, FileInfo{Path: p, Size: int64(len(fd.Content)), ModifiedAt: fd.ModifiedAt})
	}
	for d := range subdirs {
		entries = append(entries, FileInfo{Path: d, IsDir: true})
	}
	slices.SortFunc(entries, func(a, b FileInfo) int { return strings.Compare(a.Path, b.Path) })
	return LsResult{Entries: entries}
}

func (b *StateBackend) Read(ctx context.Context, path string, offset, limit int) ReadResult {
	files, _, err := b.files(ctx)
	if err != nil {
		return ReadResult{Error: err.Error()}
	}
	fd, ok := files[path]
	if !ok {
		return ReadResult{Error: fmt.Sprintf("File '%s' not found", path)}
	}
	if FileTypeForPath(path) != FileTypeText {
		return ReadResult{FileData: &fd}
	}
	res, err := SliceReadResponse(fd, offset, limit)
	if err != nil {
		return ReadResult{Error: fmt.Sprintf("Error reading file '%s': %v", path, err)}
	}
	return res
}

func (b *StateBackend) Write(ctx context.Context, path, content string) WriteResult {
	files, rc, err := b.files(ctx)
	if err != nil {
		return WriteResult{Error: err.Error()}
	}
	fd, err := b.newOrUpdated(files, path, content, EncodingUTF8)
	if err != nil {
		return WriteResult{Error: fmt.Sprintf("Error writing file '%s': %v", path, err)}
	}
	b.send(rc, map[string]*FileData{path: &fd})
	return WriteResult{Path: path}
}

func (b *StateBackend) Edit(ctx context.Context, path, oldString, newString string, replaceAll bool) EditResult {
	files, rc, err := b.files(ctx)
	if err != nil {
		return EditResult{Error: err.Error()}
	}
	fd, ok := files[path]
	if !ok {
		return EditResult{Error: fmt.Sprintf("Error: File '%s' not found", path)}
	}
	updated, n, err := PerformStringReplacement(fd.Content, oldString, newString, replaceAll)
	if err != nil {
		return EditResult{Error: err.Error()}
	}
	fd, err = UpdateFileData(fd, updated)
	if err != nil {
		return EditResult{Error: err.Error()}
	}
	b.send(rc, map[string]*FileData{path: &fd})
	return EditResult{Path: path, Occurrences: n}
}

// Delete removes the file at path and everything stored under path + "/".
func (b *StateBackend) Delete(ctx context.Context, path string) DeleteResult {
	files, rc, err := b.files(ctx)
	if err != nil {
		return DeleteResult{Error: err.Error()}
	}
	base := strings.TrimRight(path, "/")
	upd := map[string]*FileData{}
	for p := range files {
		if p == base || strings.HasPrefix(p, base+"/") {
			upd[p] = nil
		}
	}
	if len(upd) == 0 {
		return DeleteResult{Error: fmt.Sprintf("Error: File '%s' not found", path)}
	}
	b.send(rc, upd)
	return DeleteResult{Path: path}
}

func (b *StateBackend) Grep(ctx context.Context, pattern string, opts GrepOptions) GrepResult {
	files, _, err := b.files(ctx)
	if err != nil {
		return GrepResult{Error: err.Error()}
	}
	res, err := GrepMatchesFromFiles(files, pattern, opts)
	if err != nil {
		return GrepResult{Error: err.Error()}
	}
	return res
}

func (b *StateBackend) Glob(ctx context.Context, pattern, path string) GlobResult {
	files, _, err := b.files(ctx)
	if err != nil {
		return GlobResult{Error: err.Error()}
	}
	res, err := GlobSearchFiles(files, pattern, path)
	if err != nil {
		return GlobResult{Error: err.Error()}
	}
	return res
}

// UploadFiles stores UTF-8 content as text and anything else base64-encoded.
func (b *StateBackend) UploadFiles(ctx context.Context, uploads []FileUpload) []FileUploadResponse {
	out := make([]FileUploadResponse, len(uploads))
	files, rc, err := b.files(ctx)
	if err != nil {
		for i, f := range uploads {
			out[i] = FileUploadResponse{Path: f.Path, Error: ErrInvalidPath}
		}
		return out
	}
	upd := map[string]*FileData{}
	for i, f := range uploads {
		out[i] = FileUploadResponse{Path: f.Path}
		content, encoding := string(f.Content), EncodingUTF8
		if !utf8.Valid(f.Content) {
			content, encoding = base64.StdEncoding.EncodeToString(f.Content), EncodingBase64
		}
		fd, err := b.newOrUpdated(files, f.Path, content, encoding)
		if err != nil {
			out[i].Error = ErrInvalidPath
			continue
		}
		files[f.Path] = fd
		upd[f.Path] = &fd
	}
	if len(upd) > 0 {
		b.send(rc, upd)
	}
	return out
}

func (b *StateBackend) DownloadFiles(ctx context.Context, paths []string) []FileDownloadResponse {
	out := make([]FileDownloadResponse, len(paths))
	files, _, err := b.files(ctx)
	for i, p := range paths {
		out[i] = FileDownloadResponse{Path: p}
		if err != nil {
			out[i].Error = ErrInvalidPath
			continue
		}
		fd, ok := files[p]
		if !ok {
			out[i].Error = ErrFileNotFound
			continue
		}
		if fd.Encoding == EncodingBase64 {
			raw, err := base64.StdEncoding.DecodeString(fd.Content)
			if err != nil {
				out[i].Error = ErrInvalidPath
				continue
			}
			out[i].Content = raw
			continue
		}
		out[i].Content = []byte(fd.Content)
	}
	return out
}

func (b *StateBackend) newOrUpdated(files map[string]FileData, path, content, encoding string) (FileData, error) {
	if prev, ok := files[path]; ok {
		prev.Encoding = encoding
		return UpdateFileData(prev, content)
	}
	return CreateFileData(content, encoding)
}

// FilesReducer merges a files update into the current files map. Update
// entries that are nil delete their path. Both sides may also be the generic
// JSON form read back from a checkpoint. The result is always a new map.
func FilesReducer(current, update any) (any, error) {
	cur, err := filesFromValue(current)
	if err != nil {
		return nil, fmt.Errorf("current files: %w", err)
	}
	upd, err := fileUpdatesFromValue(update)
	if err != nil {
		return nil, fmt.Errorf("files update: %w", err)
	}
	out := make(map[string]FileData, len(cur)+len(upd))
	maps.Copy(out, cur)
	for p, fd := range upd {
		if fd == nil {
			delete(out, p)
			continue
		}
		out[p] = *fd
	}
	return out, nil
}

// filesFromValue normalizes a stored files value; nil entries are dropped.
func filesFromValue(v any) (map[string]FileData, error) {
	upd, err := fileUpdatesFromValue(v)
	if err != nil {
		return nil, err
	}
	out := make(map[string]FileData, len(upd))
	for p, fd := range upd {
		if fd != nil {
			out[p] = *fd
		}
	}
	return out, nil
}

func fileUpdatesFromValue(v any) (map[string]*FileData, error) {
	switch m := v.(type) {
	case nil:
		return nil, nil
	case map[string]*FileData:
		return m, nil
	case map[string]FileData:
		out := make(map[string]*FileData, len(m))
		for p, fd := range m {
			out[p] = &fd
		}
		return out, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out map[string]*FileData
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("want a map of path to file data: %w", err)
	}
	return out, nil
}
