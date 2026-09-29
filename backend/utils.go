package backend

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
)

// Shared constants from backends/utils.py.
const (
	EmptyContentWarning      = "System reminder: File exists but has empty contents"
	EmptyOldStringError      = "Error: old_string cannot be empty. Provide the exact text to replace."
	TruncationMarkerTemplate = "... [%d lines truncated] ..."
	MaxLineLength            = 5000
	ToolResultTokenLimit     = 20000
	TruncationGuidance       = "... [results truncated, try being more specific with your parameters]"
	MaxVideoInputBytes       = 1024 * 1024 * 1024
)

const maxToolCallPathComponentBytes = 128

// FileType classifies a path for read_file (text vs. multimodal).
type FileType string

const (
	FileTypeText  FileType = "text"
	FileTypeImage FileType = "image"
	FileTypeAudio FileType = "audio"
	FileTypeVideo FileType = "video"
	FileTypePDF   FileType = "pdf"
	FileTypeFile  FileType = "file"
)

var extensionToFileType = map[string]FileType{
	".png": FileTypeImage, ".jpeg": FileTypeImage, ".jpg": FileTypeImage, ".webp": FileTypeImage,
	".gif": FileTypeImage, ".heic": FileTypeImage, ".heif": FileTypeImage,
	".mp4": FileTypeVideo, ".mpeg": FileTypeVideo, ".mov": FileTypeVideo, ".avi": FileTypeVideo,
	".flv": FileTypeVideo, ".mpg": FileTypeVideo, ".webm": FileTypeVideo, ".wmv": FileTypeVideo,
	".3gpp": FileTypeVideo, ".mkv": FileTypeVideo,
	".wav": FileTypeAudio, ".mp3": FileTypeAudio, ".aiff": FileTypeAudio, ".aac": FileTypeAudio,
	".ogg": FileTypeAudio, ".flac": FileTypeAudio,
	".pdf": FileTypePDF, ".ppt": FileTypeFile, ".pptx": FileTypeFile,
}

var windowsAbsPath = regexp.MustCompile(`^[a-zA-Z]:`)

// ValidatePath normalizes a virtual path and rejects traversal or paths outside allowedPrefixes.
func ValidatePath(p string, allowedPrefixes []string) (string, error) {
	if slices.Contains(strings.Split(ToPOSIXPath(p), "/"), "..") || strings.HasPrefix(p, "~") {
		return "", fmt.Errorf("Path traversal not allowed: %s", p)
	}
	if windowsAbsPath.MatchString(p) {
		return "", fmt.Errorf("Windows absolute paths are not supported: %s. Please use virtual paths starting with / (e.g., /workspace/file.txt)", p)
	}
	normalized := path.Clean("/" + ToPOSIXPath(p))
	if allowedPrefixes != nil && !slices.ContainsFunc(allowedPrefixes, func(prefix string) bool {
		return strings.HasPrefix(normalized, prefix)
	}) {
		return "", fmt.Errorf("Path must start with one of %v: %s", allowedPrefixes, p)
	}
	return normalized, nil
}

// ToPOSIXPath converts OS-specific separators to forward slashes.
func ToPOSIXPath(p string) string { return strings.ReplaceAll(p, `\`, "/") }

// FileTypeForPath infers the FileType from the file extension.
func FileTypeForPath(p string) FileType {
	if t, ok := extensionToFileType[strings.ToLower(path.Ext(ToPOSIXPath(p)))]; ok {
		return t
	}
	return FileTypeText
}

// FormatContentWithLineNumbers renders numbered rows starting at startLine.
// Lines longer than MaxLineLength continue as N.1, N.2, ...; the marker and
// source are separated by two spaces.
func FormatContentWithLineNumbers(content string, startLine int) (string, error) {
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	type row struct{ marker, text string }
	var rows []row
	width := 0
	for i, line := range lines {
		num := strconv.Itoa(startLine + i)
		for j, chunk := range chunkRunes(line, MaxLineLength) {
			marker := num
			if j > 0 {
				marker = num + "." + strconv.Itoa(j)
			}
			rows = append(rows, row{marker, chunk})
			width = max(width, len(marker))
		}
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = fmt.Sprintf("%*s  %s", width, r.marker, r.text)
	}
	return strings.Join(out, "\n"), nil
}

// chunkRunes splits s into pieces of at most n runes; an empty s yields one empty chunk.
func chunkRunes(s string, n int) []string {
	r := []rune(s)
	if len(r) <= n {
		return []string{s}
	}
	var chunks []string
	for start := 0; start < len(r); start += n {
		chunks = append(chunks, string(r[start:min(start+n, len(r))]))
	}
	return chunks
}

// CheckEmptyContent returns EmptyContentWarning for empty content, else "".
func CheckEmptyContent(content string) string {
	if strings.TrimSpace(content) == "" {
		return EmptyContentWarning
	}
	return ""
}

func nowISO() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// CreateFileData builds FileData stamped with the current time.
func CreateFileData(content, encoding string) (FileData, error) {
	if encoding == "" {
		encoding = EncodingUTF8
	}
	if encoding != EncodingUTF8 && encoding != EncodingBase64 {
		return FileData{}, fmt.Errorf("unsupported encoding %q", encoding)
	}
	now := nowISO()
	return FileData{Content: content, Encoding: encoding, CreatedAt: now, ModifiedAt: now}, nil
}

// UpdateFileData replaces content and bumps ModifiedAt.
func UpdateFileData(fd FileData, content string) (FileData, error) {
	if fd.Encoding == "" {
		fd.Encoding = EncodingUTF8
	}
	fd.Content = content
	fd.ModifiedAt = nowISO()
	return fd, nil
}

// FileDataToString returns the stored content (base64 content is returned verbatim).
func FileDataToString(fd FileData) (string, error) { return fd.Content, nil }

// NormalizeReadBounds floors offset and limit at zero.
func NormalizeReadBounds(offset, limit int) (int, int) { return max(offset, 0), max(limit, 0) }

// SliceReadResponse slices full content into a paginated ReadResult window.
// Blank content and a zero limit leave the pagination fields unset.
func SliceReadResponse(fd FileData, offset, limit int) (ReadResult, error) {
	content, err := FileDataToString(fd)
	if err != nil {
		return ReadResult{}, err
	}
	offset, limit = NormalizeReadBounds(offset, limit)
	withContent := func(c string) *FileData {
		out := fd
		out.Content = c
		return &out
	}
	if strings.TrimSpace(content) == "" {
		return ReadResult{FileData: withContent(content)}, nil
	}
	if limit == 0 {
		return ReadResult{FileData: withContent(""), NoLinesRequested: true}, nil
	}
	lines := splitLinesKeepEnds(content)
	total := len(lines)
	if offset >= total {
		return ReadResult{Error: fmt.Sprintf("Line offset %d exceeds file length (%d lines)", offset, total)}, nil
	}
	end := min(offset+limit, total)
	sliced := strings.Join(lines[offset:end], "")
	sliced = strings.ReplaceAll(strings.ReplaceAll(sliced, "\r\n", "\n"), "\r", "\n")
	start := offset + 1
	res := ReadResult{FileData: withContent(sliced), TotalLines: &total, StartLine: &start, EndLine: &end}
	if end < total {
		next := end
		res.NextOffset = &next
	}
	return res, nil
}

// splitLinesKeepEnds splits on \n, \r\n and \r, keeping each terminator.
func splitLinesKeepEnds(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\n':
			lines = append(lines, s[start:i+1])
			start = i + 1
		case '\r':
			if i+1 < len(s) && s[i+1] == '\n' {
				i++
			}
			lines = append(lines, s[start:i+1])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// PerformStringReplacement replaces oldString with newString, requiring a
// unique match unless replaceAll. Returns the new content and occurrence count.
// Errors carry model-facing messages.
func PerformStringReplacement(content, oldString, newString string, replaceAll bool) (string, int, error) {
	if oldString == "" {
		return "", 0, errors.New(EmptyOldStringError)
	}
	occurrences := strings.Count(content, oldString)
	if occurrences == 0 {
		stripped := strings.TrimSuffix(oldString, "\n")
		if len(oldString) > 1 && stripped != oldString && strings.HasSuffix(content, stripped) {
			if n := strings.Count(content, stripped); n != 1 {
				return "", 0, fmt.Errorf("Error: old_string ends with a newline, but the file does not end with a newline. "+
					"With the trailing newline removed, old_string would appear %d times in the file. "+
					"Retry with the trailing newline removed and add surrounding context so the match is unique.", n)
			}
			return "", 0, errors.New("Error: old_string ends with a newline, but the file does not end with a newline. " +
				"Retry with the trailing newline removed from old_string (and from new_string if it also ends with a newline).")
		}
		return "", 0, fmt.Errorf("Error: String not found in file: '%s'", oldString)
	}
	if occurrences > 1 && !replaceAll {
		return "", 0, fmt.Errorf("Error: String '%s' appears %d times in file. "+
			"Use replace_all=True to replace all instances, or provide a more specific string with surrounding context.",
			oldString, occurrences)
	}
	return strings.ReplaceAll(content, oldString, newString), occurrences, nil
}

// TruncateIfTooLong clips a tool result that exceeds ToolResultTokenLimit
// (estimated at 4 characters per token).
func TruncateIfTooLong(result string) string {
	limit := ToolResultTokenLimit * 4
	r := []rune(result)
	if len(r) <= limit {
		return result
	}
	return string(r[:limit-len(TruncationGuidance)-1]) + "\n" + TruncationGuidance
}

// SanitizeToolCallID makes a tool call ID safe to use as a path component.
func SanitizeToolCallID(id string) string {
	s := strings.NewReplacer(".", "_", "/", "_", `\`, "_").Replace(id)
	if len(s) > maxToolCallPathComponentBytes {
		sum := sha256.Sum256([]byte(id))
		return "call-" + hex.EncodeToString(sum[:])
	}
	return s
}

// CompileGrepIncludeGlob compiles a glob into a matcher over search-root
// relative POSIX paths. Patterns without "/" match the basename at any depth;
// patterns with "/" match the relative path (a leading "/" anchors to the root).
func CompileGrepIncludeGlob(pattern string) (func(path string) bool, error) {
	if slices.Contains(strings.Split(ToPOSIXPath(pattern), "/"), "..") {
		return nil, fmt.Errorf("Path traversal not allowed in glob pattern %q", pattern)
	}
	anchored := strings.Contains(pattern, "/")
	compiled := strings.TrimLeft(pattern, "/")
	if !doublestar.ValidatePattern(compiled) {
		return nil, fmt.Errorf("Invalid glob pattern %q", pattern)
	}
	return func(rel string) bool {
		if !anchored {
			rel = path.Base(rel)
		}
		ok, _ := doublestar.Match(compiled, rel)
		return ok
	}, nil
}

// normalizeSearchPath returns p as "/"-rooted without a trailing slash.
func normalizeSearchPath(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if p != "/" {
		p = strings.TrimRight(p, "/")
	}
	return p
}

// relativeToRoot returns filePath relative to a normalized search root; an
// exact-file search yields the basename.
func relativeToRoot(filePath, root string) string {
	switch {
	case root == "/":
		return strings.TrimPrefix(filePath, "/")
	case filePath == root:
		return path.Base(filePath)
	default:
		return strings.TrimPrefix(filePath, root+"/")
	}
}

func filterFilesByPath(files map[string]FileData, root string) map[string]FileData {
	if fd, ok := files[root]; ok {
		return map[string]FileData{root: fd}
	}
	prefix := root
	if root != "/" {
		prefix += "/"
	}
	out := map[string]FileData{}
	for p, fd := range files {
		if strings.HasPrefix(p, prefix) {
			out[p] = fd
		}
	}
	return out
}

// GrepMatchesFromFiles searches in-memory files (StateBackend/StoreBackend grep)
// for a literal pattern.
func GrepMatchesFromFiles(files map[string]FileData, pattern string, opts GrepOptions) (GrepResult, error) {
	root := normalizeSearchPath(opts.Path)
	filtered := filterFilesByPath(files, root)
	var include func(string) bool
	if opts.Glob != "" {
		m, err := CompileGrepIncludeGlob(opts.Glob)
		if err != nil {
			return GrepResult{Error: err.Error()}, nil
		}
		include = m
	}
	paths := make([]string, 0, len(filtered))
	for p := range filtered {
		if include == nil || include(relativeToRoot(p, root)) {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	var matches []GrepMatch
	for _, p := range paths {
		for i, line := range strings.Split(filtered[p].Content, "\n") {
			if !strings.Contains(line, pattern) {
				continue
			}
			if opts.MaxCount > 0 && len(matches) >= opts.MaxCount {
				return GrepResult{Matches: matches, Truncated: true}, nil
			}
			matches = append(matches, GrepMatch{Path: p, Line: i + 1, Text: line})
		}
	}
	return GrepResult{Matches: matches}, nil
}

// GlobSearchFiles matches in-memory file paths against a glob pattern, most
// recently modified first.
func GlobSearchFiles(files map[string]FileData, pattern, searchPath string) (GlobResult, error) {
	match, err := CompileGrepIncludeGlob(pattern)
	if err != nil {
		return GlobResult{}, err
	}
	root := normalizeSearchPath(searchPath)
	var infos []FileInfo
	for p, fd := range filterFilesByPath(files, root) {
		if match(relativeToRoot(p, root)) {
			infos = append(infos, FileInfo{Path: p, Size: int64(len(fd.Content)), ModifiedAt: fd.ModifiedAt})
		}
	}
	slices.SortFunc(infos, func(a, b FileInfo) int {
		return cmp.Or(cmp.Compare(b.ModifiedAt, a.ModifiedAt), cmp.Compare(a.Path, b.Path))
	})
	return GlobResult{Matches: infos}, nil
}

// GrepOutputMode selects grep tool output formatting.
type GrepOutputMode string

const (
	GrepFilesWithMatches GrepOutputMode = "files_with_matches"
	GrepContent          GrepOutputMode = "content"
	GrepCount            GrepOutputMode = "count"
)

// FormatGrepMatches renders matches for the grep tool.
func FormatGrepMatches(matches []GrepMatch, mode GrepOutputMode) (string, error) {
	if len(matches) == 0 {
		return "No matches found", nil
	}
	byPath := map[string][]GrepMatch{}
	var paths []string
	hasContext := false
	for _, m := range matches {
		if _, ok := byPath[m.Path]; !ok {
			paths = append(paths, m.Path)
		}
		byPath[m.Path] = append(byPath[m.Path], m)
		hasContext = hasContext || len(m.ContextBefore) > 0 || len(m.ContextAfter) > 0
	}
	slices.Sort(paths)

	var out []string
	switch mode {
	case GrepFilesWithMatches:
		return strings.Join(paths, "\n"), nil
	case GrepCount:
		for _, p := range paths {
			out = append(out, fmt.Sprintf("%s: %d", p, len(byPath[p])))
		}
		return strings.Join(out, "\n"), nil
	case GrepContent:
		for _, p := range paths {
			out = append(out, p+":")
			if hasContext {
				out = append(out, formatWithContext(byPath[p])...)
				continue
			}
			for _, m := range byPath[p] {
				out = append(out, fmt.Sprintf("  %d: %s", m.Line, m.Text))
			}
		}
		return strings.Join(out, "\n"), nil
	default:
		return "", fmt.Errorf("unknown grep output mode %q", mode)
	}
}

// formatWithContext renders one file's matches grep -C style: ":" marks a
// match, "-" a context line, and "--" separates non-adjacent groups.
func formatWithContext(matches []GrepMatch) []string {
	matching := map[int]bool{}
	displayed := map[int]string{}
	for _, m := range matches {
		for _, c := range m.ContextBefore {
			displayed[c.Line] = c.Text
		}
		displayed[m.Line] = m.Text
		matching[m.Line] = true
		for _, c := range m.ContextAfter {
			displayed[c.Line] = c.Text
		}
	}
	nums := make([]int, 0, len(displayed))
	for n := range displayed {
		nums = append(nums, n)
	}
	slices.Sort(nums)
	var out []string
	for i, n := range nums {
		if i > 0 && n > nums[i-1]+1 {
			out = append(out, "  --")
		}
		sep := "-"
		if matching[n] {
			sep = ":"
		}
		out = append(out, fmt.Sprintf("  %d%s %s", n, sep, displayed[n]))
	}
	return out
}

var regexSignal = regexp.MustCompile(`\||\.\*|\.\+|\\[.wWdDsSbB(){}\[\]|+*?^$]`)

// RegexLiteralHint returns guidance when a literal grep pattern looks like a regex.
func RegexLiteralHint(pattern string) string {
	if !regexSignal.MatchString(pattern) {
		return ""
	}
	return "Note: grep matches literal text, not regex, so characters like " +
		"`|`, `.*`, and `\\.` are searched verbatim. Search for the literal " +
		"text you need instead; for `|` alternation, run a separate search " +
		"per alternative."
}
