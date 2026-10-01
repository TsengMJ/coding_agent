package backend

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/tsengmingchun/deepagents_golang/runtime"
)

// NamespaceFactory derives the store namespace for a run (e.g. per user).
type NamespaceFactory func(ctx context.Context, rc *RunContext) (runtime.Namespace, error)

// StaticNamespace always uses ns.
func StaticNamespace(ns runtime.Namespace) NamespaceFactory {
	return func(context.Context, *RunContext) (runtime.Namespace, error) { return ns, nil }
}

// defaultStoreNamespace is used when no NamespaceFactory is given.
var defaultStoreNamespace = runtime.Namespace{"filesystem"}

// StoreBackend persists files in a runtime.Store, durable across threads
// (Python `StoreBackend`). Each file is one item keyed by its path whose
// value holds the FileData fields.
type StoreBackend struct {
	namespace NamespaceFactory
	// store overrides the run's store when non-nil.
	store runtime.Store
	// notFoundHint is appended to "not found" errors, e.g. to say that
	// files in an expiring store may have expired.
	notFoundHint string
}

// NewStoreBackend returns a store-backed file store. store may be nil to use
// the store from the RunContext at call time; namespace may be nil for
// {"filesystem"}.
func NewStoreBackend(namespace NamespaceFactory, store runtime.Store) *StoreBackend {
	if namespace == nil {
		namespace = StaticNamespace(defaultStoreNamespace)
	}
	return &StoreBackend{namespace: namespace, store: store}
}

// WithNotFoundHint returns b with hint appended to every "not found" error.
func (b *StoreBackend) WithNotFoundHint(hint string) *StoreBackend {
	c := *b
	c.notFoundHint = hint
	return &c
}

// ValidateNamespace rejects empty components, wildcards and unsafe characters.
func ValidateNamespace(ns runtime.Namespace) error { return runtime.ValidateNamespace(ns) }

func (b *StoreBackend) target(ctx context.Context) (runtime.Store, runtime.Namespace, error) {
	rc := RunContextFrom(ctx)
	st := b.store
	if st == nil && rc != nil {
		st = rc.Store
	}
	if st == nil {
		return nil, nil, errors.New("StoreBackend has no store: pass one to NewStoreBackend or run inside an agent with a store")
	}
	ns, err := b.namespace(ctx, rc)
	if err != nil {
		return nil, nil, err
	}
	if err := ValidateNamespace(ns); err != nil {
		return nil, nil, err
	}
	return st, ns, nil
}

func (b *StoreBackend) notFound(format, path string) string {
	msg := fmt.Sprintf(format, path)
	if b.notFoundHint != "" {
		msg += ". " + b.notFoundHint
	}
	return msg
}

func fileDataFromItem(it *runtime.Item) (FileData, error) {
	raw, err := json.Marshal(it.Value)
	if err != nil {
		return FileData{}, err
	}
	var fd FileData
	if err := json.Unmarshal(raw, &fd); err != nil {
		return FileData{}, fmt.Errorf("stored file %s: %w", it.Key, err)
	}
	return fd, nil
}

func fileDataValue(fd FileData) map[string]any {
	return map[string]any{"content": fd.Content, "encoding": fd.Encoding, "created_at": fd.CreatedAt, "modified_at": fd.ModifiedAt}
}

func (b *StoreBackend) get(ctx context.Context, path string) (*FileData, error) {
	st, ns, err := b.target(ctx)
	if err != nil {
		return nil, err
	}
	it, err := st.Get(ctx, ns, path)
	if err != nil || it == nil {
		return nil, err
	}
	fd, err := fileDataFromItem(it)
	return &fd, err
}

func (b *StoreBackend) put(ctx context.Context, path string, fd FileData) error {
	st, ns, err := b.target(ctx)
	if err != nil {
		return err
	}
	return st.Put(ctx, ns, path, fileDataValue(fd))
}

// all returns every file in the namespace by path.
func (b *StoreBackend) all(ctx context.Context) (map[string]FileData, error) {
	st, ns, err := b.target(ctx)
	if err != nil {
		return nil, err
	}
	items, err := st.Search(ctx, ns, runtime.SearchOptions{})
	if err != nil {
		return nil, err
	}
	out := make(map[string]FileData, len(items))
	for _, it := range items {
		if !slices.Equal(it.Namespace, ns) {
			continue
		}
		fd, err := fileDataFromItem(&it)
		if err != nil {
			return nil, err
		}
		out[it.Key] = fd
	}
	return out, nil
}

func (b *StoreBackend) Ls(ctx context.Context, path string) LsResult {
	files, err := b.all(ctx)
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

func (b *StoreBackend) Read(ctx context.Context, path string, offset, limit int) ReadResult {
	fd, err := b.get(ctx, path)
	if err != nil {
		return ReadResult{Error: err.Error()}
	}
	if fd == nil {
		return ReadResult{Error: b.notFound("File '%s' not found", path)}
	}
	if FileTypeForPath(path) != FileTypeText {
		return ReadResult{FileData: fd}
	}
	res, err := SliceReadResponse(*fd, offset, limit)
	if err != nil {
		return ReadResult{Error: fmt.Sprintf("Error reading file '%s': %v", path, err)}
	}
	return res
}

// Write creates or overwrites a file.
func (b *StoreBackend) Write(ctx context.Context, path, content string) WriteResult {
	fd, err := b.newOrUpdated(ctx, path, content, EncodingUTF8)
	if err == nil {
		err = b.put(ctx, path, fd)
	}
	if err != nil {
		return WriteResult{Error: fmt.Sprintf("Error writing file '%s': %v", path, err)}
	}
	return WriteResult{Path: path}
}

func (b *StoreBackend) newOrUpdated(ctx context.Context, path, content, encoding string) (FileData, error) {
	prev, err := b.get(ctx, path)
	if err != nil {
		return FileData{}, err
	}
	if prev != nil {
		prev.Encoding = encoding
		return UpdateFileData(*prev, content)
	}
	return CreateFileData(content, encoding)
}

func (b *StoreBackend) Edit(ctx context.Context, path, oldString, newString string, replaceAll bool) EditResult {
	fd, err := b.get(ctx, path)
	if err != nil {
		return EditResult{Error: err.Error()}
	}
	if fd == nil {
		return EditResult{Error: b.notFound("Error: File '%s' not found", path)}
	}
	updated, n, err := PerformStringReplacement(fd.Content, oldString, newString, replaceAll)
	if err != nil {
		return EditResult{Error: err.Error()}
	}
	next, err := UpdateFileData(*fd, updated)
	if err == nil {
		err = b.put(ctx, path, next)
	}
	if err != nil {
		return EditResult{Error: err.Error()}
	}
	return EditResult{Path: path, Occurrences: n}
}

// Delete removes the file at path and everything stored under path + "/".
func (b *StoreBackend) Delete(ctx context.Context, path string) DeleteResult {
	files, err := b.all(ctx)
	if err != nil {
		return DeleteResult{Error: err.Error()}
	}
	st, ns, err := b.target(ctx)
	if err != nil {
		return DeleteResult{Error: err.Error()}
	}
	base := strings.TrimRight(path, "/")
	deleted := false
	for p := range files {
		if p == base || strings.HasPrefix(p, base+"/") {
			if err := st.Delete(ctx, ns, p); err != nil {
				return DeleteResult{Error: err.Error()}
			}
			deleted = true
		}
	}
	if !deleted {
		return DeleteResult{Error: b.notFound("Error: File '%s' not found", path)}
	}
	return DeleteResult{Path: path}
}

func (b *StoreBackend) Grep(ctx context.Context, pattern string, opts GrepOptions) GrepResult {
	files, err := b.all(ctx)
	if err != nil {
		return GrepResult{Error: err.Error()}
	}
	res, err := GrepMatchesFromFiles(files, pattern, opts)
	if err != nil {
		return GrepResult{Error: err.Error()}
	}
	return res
}

func (b *StoreBackend) Glob(ctx context.Context, pattern, path string) GlobResult {
	files, err := b.all(ctx)
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
func (b *StoreBackend) UploadFiles(ctx context.Context, uploads []FileUpload) []FileUploadResponse {
	out := make([]FileUploadResponse, len(uploads))
	for i, f := range uploads {
		out[i] = FileUploadResponse{Path: f.Path}
		content, encoding := string(f.Content), EncodingUTF8
		if !utf8.Valid(f.Content) {
			content, encoding = base64.StdEncoding.EncodeToString(f.Content), EncodingBase64
		}
		fd, err := b.newOrUpdated(ctx, f.Path, content, encoding)
		if err == nil {
			err = b.put(ctx, f.Path, fd)
		}
		if err != nil {
			out[i].Error = ErrInvalidPath
		}
	}
	return out
}

func (b *StoreBackend) DownloadFiles(ctx context.Context, paths []string) []FileDownloadResponse {
	out := make([]FileDownloadResponse, len(paths))
	for i, p := range paths {
		out[i] = FileDownloadResponse{Path: p}
		fd, err := b.get(ctx, p)
		switch {
		case err != nil:
			out[i].Error = ErrInvalidPath
		case fd == nil:
			out[i].Error = ErrFileNotFound
		case fd.Encoding == EncodingBase64:
			raw, err := base64.StdEncoding.DecodeString(fd.Content)
			if err != nil {
				out[i].Error = ErrInvalidPath
				continue
			}
			out[i].Content = raw
		default:
			out[i].Content = []byte(fd.Content)
		}
	}
	return out
}
