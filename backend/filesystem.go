package backend

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	pathpkg "path"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultMaxFileSizeMB = 10
	defaultGlobTimeout   = 20 * time.Second
)

// FilesystemOptions configures FilesystemBackend.
type FilesystemOptions struct {
	// RootDir defaults to the current working directory.
	RootDir string
	// VirtualMode treats RootDir as "/" and blocks escapes (default true in Python).
	VirtualMode bool
	// MaxFileSizeMB caps searchable and readable file size (default 10).
	MaxFileSizeMB int
}

// FilesystemBackend reads and writes the local disk. Grep is a literal
// substring search implemented in Go; walks skip ".git" directories.
type FilesystemBackend struct {
	opts FilesystemOptions
	// root is the absolute, symlink-resolved RootDir.
	root    string
	rootErr error
}

// NewFilesystemBackend returns a local-disk backend. A RootDir that cannot be
// resolved is reported by every operation.
func NewFilesystemBackend(opts FilesystemOptions) *FilesystemBackend {
	if opts.MaxFileSizeMB <= 0 {
		opts.MaxFileSizeMB = defaultMaxFileSizeMB
	}
	b := &FilesystemBackend{opts: opts}
	b.root, b.rootErr = resolveRoot(opts.RootDir)
	return b
}

func resolveRoot(dir string) (string, error) {
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve root dir: %w", err)
		}
		dir = wd
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve root dir %q: %w", dir, err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve root dir %q: %w", dir, err)
	}
	return real, nil
}

// Root returns the resolved root directory.
func (b *FilesystemBackend) Root() string { return b.root }

// resolve maps a caller path to an absolute host path. In virtual mode the
// path is rooted at RootDir and must stay inside it after following symlinks.
func (b *FilesystemBackend) resolve(p string) (string, error) {
	if b.rootErr != nil {
		return "", b.rootErr
	}
	if !b.opts.VirtualMode {
		if filepath.IsAbs(p) {
			return filepath.Clean(p), nil
		}
		return filepath.Join(b.root, p), nil
	}
	vpath := ToPOSIXPath(p)
	if strings.Contains(vpath, "..") || strings.HasPrefix(vpath, "~") {
		return "", errors.New("Path traversal not allowed")
	}
	full, err := evalExisting(filepath.Join(b.root, filepath.FromSlash(strings.TrimLeft(vpath, "/"))))
	if err != nil {
		return "", err
	}
	if !within(b.root, full) {
		return "", fmt.Errorf("Path:%s outside root directory", p)
	}
	return full, nil
}

// resolveNoFollow is resolve without following a symlink in the last path
// element, so operations on a link act on the link itself.
func (b *FilesystemBackend) resolveNoFollow(p string) (string, error) {
	vpath := ToPOSIXPath(p)
	trimmed := strings.TrimRight(vpath, "/")
	if trimmed == "" {
		return b.resolve(vpath)
	}
	dir, name := pathpkg.Split(trimmed)
	if name == "." || name == ".." {
		return "", errors.New("Path traversal not allowed")
	}
	parent, err := b.resolve(dir)
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, name), nil
}

// evalExisting resolves symlinks in the longest existing prefix of p and
// appends the non-existent remainder.
func evalExisting(p string) (string, error) {
	var rest []string
	cur := p
	for {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			return filepath.Join(append([]string{real}, rest...)...), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p, nil
		}
		rest = append([]string{filepath.Base(cur)}, rest...)
		cur = parent
	}
}

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// display renders a host path for the model: virtual paths in virtual mode.
func (b *FilesystemBackend) display(p string) string {
	if !b.opts.VirtualMode {
		return p
	}
	rel, err := filepath.Rel(b.root, p)
	if err != nil || rel == "." {
		return "/"
	}
	return "/" + filepath.ToSlash(rel)
}

func fileInfo(path string, st fs.FileInfo) FileInfo {
	info := FileInfo{Path: path, ModifiedAt: st.ModTime().Format(time.RFC3339)}
	if st.IsDir() {
		info.Path += "/"
		info.IsDir = true
	} else {
		info.Size = st.Size()
	}
	return info
}

// Ls lists direct children; directories end with "/".
func (b *FilesystemBackend) Ls(ctx context.Context, path string) LsResult {
	dir, err := b.resolve(path)
	if err != nil {
		return LsResult{Error: fmt.Sprintf("Cannot list '%s': %v", path, err)}
	}
	st, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return LsResult{Error: fmt.Sprintf("Path '%s': path_not_found", path)}
	}
	if err != nil {
		return LsResult{Error: fmt.Sprintf("Cannot list '%s': %s", path, errText(err))}
	}
	if !st.IsDir() {
		return LsResult{Error: fmt.Sprintf("Path '%s': not_a_directory", path)}
	}
	children, err := os.ReadDir(dir)
	if err != nil {
		return LsResult{Error: fmt.Sprintf("Listing of '%s' aborted: %s", path, errText(err))}
	}
	entries := make([]FileInfo, 0, len(children))
	for _, c := range children {
		child := filepath.Join(dir, c.Name())
		st, err := os.Stat(child)
		if err != nil {
			continue
		}
		if b.opts.VirtualMode {
			if real, err := filepath.EvalSymlinks(child); err != nil || !within(b.root, real) {
				continue
			}
		}
		entries = append(entries, fileInfo(b.display(child), st))
	}
	slices.SortFunc(entries, func(a, b FileInfo) int { return strings.Compare(a.Path, b.Path) })
	return LsResult{Entries: entries}
}

// Read returns a raw window of a text file, or the whole file base64-encoded
// for non-text types.
func (b *FilesystemBackend) Read(ctx context.Context, path string, offset, limit int) ReadResult {
	full, err := b.resolve(path)
	if err != nil {
		return ReadResult{Error: fmt.Sprintf("Error reading file '%s': %v", path, err)}
	}
	st, err := os.Stat(full)
	if err != nil || !st.Mode().IsRegular() {
		return ReadResult{Error: fmt.Sprintf("File '%s' not found", path)}
	}
	if limit := int64(b.opts.MaxFileSizeMB) * 1024 * 1024; st.Size() > limit {
		return ReadResult{Error: fmt.Sprintf("Error reading file '%s': file is %d bytes, over the %d MB read limit. Use grep to find the relevant part.",
			path, st.Size(), b.opts.MaxFileSizeMB)}
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		return ReadResult{Error: fmt.Sprintf("Error reading file '%s': %s", path, errText(err))}
	}
	if FileTypeForPath(path) != FileTypeText {
		return ReadResult{FileData: &FileData{Content: base64.StdEncoding.EncodeToString(raw), Encoding: EncodingBase64}}
	}
	if !utf8.Valid(raw) {
		return ReadResult{Error: fmt.Sprintf("Error reading file '%s': file is not valid UTF-8", path)}
	}
	content := string(raw)
	if msg := CheckEmptyContent(content); msg != "" {
		return ReadResult{FileData: &FileData{Content: msg, Encoding: EncodingUTF8}}
	}
	res, err := SliceReadResponse(FileData{Content: content, Encoding: EncodingUTF8}, offset, limit)
	if err != nil {
		return ReadResult{Error: fmt.Sprintf("Error reading file '%s': %v", path, err)}
	}
	return res
}

// Write creates or overwrites a file, creating parent directories.
func (b *FilesystemBackend) Write(ctx context.Context, path, content string) WriteResult {
	full, err := b.resolve(path)
	if err != nil {
		return WriteResult{Error: fmt.Sprintf("Error writing file '%s': %v", path, err)}
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return WriteResult{Error: fmt.Sprintf("Error writing file '%s': %s", path, errText(err))}
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		return WriteResult{Error: fmt.Sprintf("Error writing file '%s': %s", path, errText(err))}
	}
	return WriteResult{Path: path}
}

// Edit replaces oldString with newString (see PerformStringReplacement).
func (b *FilesystemBackend) Edit(ctx context.Context, path, oldString, newString string, replaceAll bool) EditResult {
	full, err := b.resolve(path)
	if err != nil {
		return EditResult{Error: fmt.Sprintf("Error editing file '%s': %v", path, err)}
	}
	st, err := os.Stat(full)
	if err != nil || !st.Mode().IsRegular() {
		return EditResult{Error: fmt.Sprintf("Error: File '%s' not found", path)}
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		return EditResult{Error: fmt.Sprintf("Error editing file '%s': %s", path, errText(err))}
	}
	normalize := strings.NewReplacer("\r\n", "\n", "\r", "\n")
	updated, n, err := PerformStringReplacement(string(raw), normalize.Replace(oldString), normalize.Replace(newString), replaceAll)
	if err != nil {
		return EditResult{Error: err.Error()}
	}
	if err := os.WriteFile(full, []byte(updated), st.Mode().Perm()); err != nil {
		return EditResult{Error: fmt.Sprintf("Error editing file '%s': %s", path, errText(err))}
	}
	return EditResult{Path: path, Occurrences: n}
}

// Delete removes a file, a symlink (not its target) or a directory tree.
func (b *FilesystemBackend) Delete(ctx context.Context, path string) DeleteResult {
	full, err := b.resolveNoFollow(path)
	if err != nil {
		return DeleteResult{Error: fmt.Sprintf("Error deleting '%s': %v", path, err)}
	}
	if full == b.root {
		return DeleteResult{Error: fmt.Sprintf("Error deleting '%s': refusing to delete the root directory", path)}
	}
	st, err := os.Lstat(full)
	if err != nil {
		return DeleteResult{Error: fmt.Sprintf("Error: '%s' not found", path)}
	}
	remove := os.RemoveAll
	if st.Mode()&fs.ModeSymlink != 0 {
		remove = os.Remove
	}
	if err := remove(full); err != nil {
		return DeleteResult{Error: fmt.Sprintf("Error deleting '%s': %s", path, errText(err))}
	}
	return DeleteResult{Path: path}
}

// Grep searches files under opts.Path for a literal substring.
func (b *FilesystemBackend) Grep(ctx context.Context, pattern string, opts GrepOptions) GrepResult {
	base := b.root
	if opts.Path != "" && opts.Path != "/" {
		p, err := b.resolve(opts.Path)
		if err != nil {
			return GrepResult{Error: fmt.Sprintf("Error searching path '%s': %v", opts.Path, err)}
		}
		base = p
	}
	var include func(string) bool
	if opts.Glob != "" {
		m, err := CompileGrepIncludeGlob(opts.Glob)
		if err != nil {
			return GrepResult{Error: err.Error()}
		}
		include = m
	}
	ctx, cancel := context.WithTimeout(ctx, DefaultGrepTimeout)
	defer cancel()

	root := base
	if st, err := os.Stat(base); err == nil && !st.IsDir() {
		root = filepath.Dir(base)
	}
	maxSize := int64(b.opts.MaxFileSizeMB) * 1024 * 1024
	var matches []GrepMatch
	truncated := false
	errStop := errors.New("stop")
	walkErr := b.walkFiles(ctx, base, func(p string, st fs.FileInfo) error {
		if include != nil {
			rel, _ := filepath.Rel(root, p)
			if !include(filepath.ToSlash(rel)) {
				return nil
			}
		}
		if st.Size() > maxSize {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil || !utf8.Valid(raw) {
			return nil
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if !strings.Contains(line, pattern) {
				continue
			}
			if opts.MaxCount > 0 && len(matches) >= opts.MaxCount {
				truncated = true
				return errStop
			}
			matches = append(matches, GrepMatch{Path: b.display(p), Line: i + 1, Text: strings.TrimSuffix(line, "\r")})
		}
		return nil
	})
	switch {
	case errors.Is(walkErr, context.DeadlineExceeded):
		truncated = true
	case walkErr != nil && !errors.Is(walkErr, errStop):
		return GrepResult{Matches: matches, Error: fmt.Sprintf("Grep of '%s' aborted: %s", opts.Path, errText(walkErr))}
	}
	return GrepResult{Matches: matches, Truncated: truncated}
}

// Glob finds regular files under path matching pattern, sorted by path.
func (b *FilesystemBackend) Glob(ctx context.Context, pattern, path string) GlobResult {
	match, err := CompileGrepIncludeGlob(pattern)
	if err != nil {
		return GlobResult{Error: err.Error()}
	}
	base := b.root
	if path != "" && path != "/" {
		p, err := b.resolve(path)
		if err != nil {
			return GlobResult{Error: fmt.Sprintf("Error globbing path '%s': %v", path, err)}
		}
		base = p
	}
	if st, err := os.Stat(base); err != nil || !st.IsDir() {
		return GlobResult{}
	}
	ctx, cancel := context.WithTimeout(ctx, defaultGlobTimeout)
	defer cancel()

	var infos []FileInfo
	walkErr := b.walkFiles(ctx, base, func(p string, st fs.FileInfo) error {
		rel, _ := filepath.Rel(base, p)
		if match(filepath.ToSlash(rel)) {
			infos = append(infos, fileInfo(b.display(p), st))
		}
		return nil
	})
	slices.SortFunc(infos, func(a, b FileInfo) int { return strings.Compare(a.Path, b.Path) })
	switch {
	case errors.Is(walkErr, context.DeadlineExceeded):
		return GlobResult{Matches: infos, Truncated: true, TruncationReason: GlobTruncatedBudget}
	case walkErr != nil:
		return GlobResult{Matches: infos, Error: fmt.Sprintf("Glob of '%s' aborted partway: %s", path, errText(walkErr))}
	}
	return GlobResult{Matches: infos}
}

// walkFiles calls fn for every regular file under base (or base itself when
// it is a file), skipping ".git" and, in virtual mode, entries that resolve
// outside the root. Unreadable entries are skipped.
func (b *FilesystemBackend) walkFiles(ctx context.Context, base string, fn func(string, fs.FileInfo) error) error {
	return filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			if d != nil && d.IsDir() && p != base {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" && p != base {
				return fs.SkipDir
			}
			return nil
		}
		st, err := os.Stat(p)
		if err != nil || !st.Mode().IsRegular() {
			return nil
		}
		if b.opts.VirtualMode && d.Type()&fs.ModeSymlink != 0 {
			if real, err := filepath.EvalSymlinks(p); err != nil || !within(b.root, real) {
				return nil
			}
		}
		return fn(p, st)
	})
}

// UploadFiles writes raw bytes to each path.
func (b *FilesystemBackend) UploadFiles(ctx context.Context, files []FileUpload) []FileUploadResponse {
	out := make([]FileUploadResponse, len(files))
	for i, f := range files {
		out[i] = FileUploadResponse{Path: f.Path}
		full, err := b.resolve(f.Path)
		if err == nil {
			err = os.MkdirAll(filepath.Dir(full), 0o755)
		}
		if err == nil {
			err = os.WriteFile(full, f.Content, 0o644)
		}
		if err != nil {
			out[i].Error = errorCode(err)
		}
	}
	return out
}

// DownloadFiles reads raw bytes from each path.
func (b *FilesystemBackend) DownloadFiles(ctx context.Context, paths []string) []FileDownloadResponse {
	out := make([]FileDownloadResponse, len(paths))
	for i, p := range paths {
		out[i] = FileDownloadResponse{Path: p}
		full, err := b.resolve(p)
		if err == nil {
			out[i].Content, err = os.ReadFile(full)
		}
		if err != nil {
			out[i].Error = errorCode(err)
		}
	}
	return out
}

// errorCode maps an error to a standardized FileOperationError code.
func errorCode(err error) string {
	var pathErr *fs.PathError
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return ErrFileNotFound
	case errors.Is(err, fs.ErrPermission):
		return ErrPermissionDenied
	case errors.As(err, &pathErr) && strings.Contains(pathErr.Err.Error(), "is a directory"):
		return ErrIsDirectory
	default:
		return ErrInvalidPath
	}
}

// errText hides host paths embedded in *fs.PathError messages.
func errText(err error) string {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err.Error()
	}
	return err.Error()
}
