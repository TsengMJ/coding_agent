package codetools

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// OpKind is the kind of file operation in a patch.
type OpKind string

const (
	OpAdd    OpKind = "add"
	OpDelete OpKind = "delete"
	OpUpdate OpKind = "update"
)

// FileOp is one file section of a Codex-style patch.
type FileOp struct {
	Kind OpKind
	Path string
	// MoveTo renames the file after an update.
	MoveTo string
	// Content is the full text of an added file.
	Content string
	Chunks  []Chunk
}

// Chunk is one "@@" hunk of an update.
type Chunk struct {
	// Context is the text after "@@", located before the hunk is matched.
	Context string
	Old     []string
	New     []string
	// EOF anchors the hunk to the end of the file ("*** End of File").
	EOF bool
	// Line is the 1-based patch line where the hunk starts, for errors.
	Line int
}

const (
	beginPatch  = "*** Begin Patch"
	endPatch    = "*** End Patch"
	addFile     = "*** Add File:"
	deleteFile  = "*** Delete File:"
	updateFile  = "*** Update File:"
	moveTo      = "*** Move to:"
	endOfFile   = "*** End of File"
	hunkMarker  = "@@"
	patchPrefix = "*** "
)

// ParsePatch parses a patch in the Codex apply_patch format:
//
//	*** Begin Patch
//	*** Update File: path
//	@@ optional context line
//	 context
//	-removed
//	+added
//	*** End Patch
func ParsePatch(text string) ([]FileOp, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	lines = stripHeredoc(trimBlankLines(lines))
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != beginPatch {
		return nil, fmt.Errorf("line 1: patch must start with %q", beginPatch)
	}
	if strings.TrimSpace(lines[len(lines)-1]) != endPatch {
		return nil, fmt.Errorf("line %d: patch must end with %q", len(lines), endPatch)
	}
	p := &patchParser{lines: lines[:len(lines)-1], i: 1}
	var ops []FileOp
	for p.i < len(p.lines) {
		op, err := p.fileOp()
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	if len(ops) == 0 {
		return nil, errors.New("patch contains no file operations")
	}
	seen := map[string]bool{}
	for _, op := range ops {
		for _, path := range []string{op.Path, op.MoveTo} {
			if path == "" {
				continue
			}
			if seen[path] {
				return nil, fmt.Errorf("file %q appears more than once in the patch; combine its changes into one section", path)
			}
			seen[path] = true
		}
	}
	return ops, nil
}

func trimBlankLines(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// stripHeredoc accepts a patch pasted as a shell heredoc (<<'EOF' ... EOF).
func stripHeredoc(lines []string) []string {
	if len(lines) >= 2 && strings.HasPrefix(strings.TrimSpace(lines[0]), "<<") {
		tag := strings.Trim(strings.TrimPrefix(strings.TrimSpace(lines[0]), "<<"), `'"- `)
		if strings.TrimSpace(lines[len(lines)-1]) == tag {
			return trimBlankLines(lines[1 : len(lines)-1])
		}
	}
	return lines
}

type patchParser struct {
	lines []string
	i     int
}

func (p *patchParser) errorf(format string, a ...any) error {
	return fmt.Errorf("line %d: %s", p.i+1, fmt.Sprintf(format, a...))
}

func (p *patchParser) fileOp() (FileOp, error) {
	line := strings.TrimRight(p.lines[p.i], " \t")
	switch {
	case strings.HasPrefix(line, addFile):
		op := FileOp{Kind: OpAdd, Path: strings.TrimSpace(strings.TrimPrefix(line, addFile))}
		p.i++
		var body []string
		for p.i < len(p.lines) && !strings.HasPrefix(p.lines[p.i], patchPrefix) {
			l := p.lines[p.i]
			if !strings.HasPrefix(l, "+") {
				return op, p.errorf("every line of an added file must start with '+', got %q", l)
			}
			body = append(body, l[1:])
			p.i++
		}
		if len(body) > 0 {
			op.Content = strings.Join(body, "\n") + "\n"
		}
		return op, requirePath(op.Path, p)
	case strings.HasPrefix(line, deleteFile):
		op := FileOp{Kind: OpDelete, Path: strings.TrimSpace(strings.TrimPrefix(line, deleteFile))}
		p.i++
		return op, requirePath(op.Path, p)
	case strings.HasPrefix(line, updateFile):
		op := FileOp{Kind: OpUpdate, Path: strings.TrimSpace(strings.TrimPrefix(line, updateFile))}
		if err := requirePath(op.Path, p); err != nil {
			return op, err
		}
		p.i++
		if p.i < len(p.lines) && strings.HasPrefix(p.lines[p.i], moveTo) {
			op.MoveTo = strings.TrimSpace(strings.TrimPrefix(p.lines[p.i], moveTo))
			if err := requirePath(op.MoveTo, p); err != nil {
				return op, err
			}
			p.i++
		}
		for p.i < len(p.lines) && !isFileHeader(p.lines[p.i]) {
			c, err := p.chunk(len(op.Chunks) == 0)
			if err != nil {
				return op, err
			}
			op.Chunks = append(op.Chunks, c)
		}
		if len(op.Chunks) == 0 && op.MoveTo == "" {
			return op, fmt.Errorf("update of %q has no hunks", op.Path)
		}
		return op, nil
	}
	return FileOp{}, p.errorf("expected '*** Add File:', '*** Delete File:' or '*** Update File:', got %q", line)
}

func requirePath(path string, p *patchParser) error {
	if path == "" {
		return p.errorf("missing file path")
	}
	return nil
}

func isFileHeader(line string) bool {
	return strings.HasPrefix(line, addFile) || strings.HasPrefix(line, deleteFile) || strings.HasPrefix(line, updateFile)
}

// chunk parses one hunk; the first hunk of a file may omit its "@@" line.
func (p *patchParser) chunk(first bool) (Chunk, error) {
	c := Chunk{Line: p.i + 1}
	line := p.lines[p.i]
	switch {
	case strings.TrimSpace(line) == hunkMarker:
		p.i++
	case strings.HasPrefix(line, hunkMarker+" "):
		c.Context = strings.TrimPrefix(line, hunkMarker+" ")
		p.i++
	case !first:
		return c, p.errorf("expected '@@' to start the next hunk, got %q", line)
	}
	changed := false
	for p.i < len(p.lines) {
		l := p.lines[p.i]
		if isFileHeader(l) || strings.HasPrefix(l, hunkMarker) {
			break
		}
		if strings.TrimSpace(l) == endOfFile {
			c.EOF = true
			p.i++
			break
		}
		switch {
		case l == "":
			c.Old, c.New = append(c.Old, ""), append(c.New, "")
		case l[0] == ' ':
			c.Old, c.New = append(c.Old, l[1:]), append(c.New, l[1:])
		case l[0] == '-':
			c.Old = append(c.Old, l[1:])
			changed = true
		case l[0] == '+':
			c.New = append(c.New, l[1:])
			changed = true
		default:
			return c, p.errorf("hunk lines must start with ' ', '-' or '+', got %q", l)
		}
		p.i++
	}
	if !changed {
		return c, fmt.Errorf("line %d: hunk has no '-' or '+' lines; every hunk must remove or add something. "+
			"Prefix each line with ' ' (unchanged context), '-' (remove) or '+' (add), e.g.:\n"+
			"@@ def total(items):\n     subtotal = sum(items)\n-    return subtotal\n+    return round(subtotal, 2)", c.Line)
	}
	return c, nil
}

// closestHint points at the file line most like the first non-blank line of
// a hunk that did not match, so the model can re-read around it.
func closestHint(lines, old []string) string {
	target := ""
	for _, l := range old {
		if strings.TrimSpace(l) != "" {
			target = strings.TrimSpace(l)
			break
		}
	}
	if target == "" {
		return ""
	}
	best, bestScore := -1, 0.4
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == target {
			return fmt.Sprintf("\nThe first line matches file line %d, so a later line differs (whitespace or stale content); re-read the file from line %d and copy the lines exactly.", i+1, i+1)
		}
		if s := wordOverlap(t, target); s > bestScore {
			best, bestScore = i, s
		}
	}
	if best < 0 {
		return "\nNo similar line was found; re-read the file before retrying."
	}
	return fmt.Sprintf("\nThe closest file line is %d: %q; re-read the file around it and copy the lines exactly.", best+1, clipHint(lines[best]))
}

// wordOverlap is the Jaccard similarity of the identifiers in a and b.
func wordOverlap(a, b string) float64 {
	notWord := func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' }
	wa, wb := strings.FieldsFunc(a, notWord), strings.FieldsFunc(b, notWord)
	if len(wa) == 0 || len(wb) == 0 {
		return 0
	}
	set := make(map[string]bool, len(wa))
	for _, w := range wa {
		set[w] = true
	}
	common, union := 0, len(set)
	seen := map[string]bool{}
	for _, w := range wb {
		if seen[w] {
			continue
		}
		seen[w] = true
		if set[w] {
			common++
		} else {
			union++
		}
	}
	return float64(common) / float64(union)
}

func clipHint(s string) string {
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

// applyChunks applies an update's hunks to content. Hunks are matched in
// order, each starting after the previous one.
func applyChunks(content string, chunks []Chunk) (string, error) {
	crlf := strings.Contains(content, "\r\n")
	if crlf {
		content = strings.ReplaceAll(content, "\r\n", "\n")
	}
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	type replacement struct {
		start, n int
		repl     []string
	}
	var reps []replacement
	pos := 0
	for _, c := range chunks {
		if c.Context != "" {
			idx, _ := seekSequence(lines, []string{c.Context}, pos, false)
			if idx < 0 {
				return "", fmt.Errorf("hunk at patch line %d: context line %q not found", c.Line, c.Context)
			}
			pos = idx + 1
		}
		if len(c.Old) == 0 {
			at := len(lines)
			if !c.EOF && c.Context != "" {
				at = pos
			}
			reps = append(reps, replacement{at, 0, c.New})
			continue
		}
		old, repl := c.Old, c.New
		idx, n := seekSequence(lines, old, pos, c.EOF)
		if idx < 0 && len(old) > 1 && old[len(old)-1] == "" {
			old = old[:len(old)-1]
			if len(repl) > 0 && repl[len(repl)-1] == "" {
				repl = repl[:len(repl)-1]
			}
			idx, n = seekSequence(lines, old, pos, c.EOF)
		}
		if idx < 0 {
			return "", fmt.Errorf("hunk at patch line %d: could not find these lines in the file:\n%s%s", c.Line, strings.Join(old, "\n"), closestHint(lines, old))
		}
		if n > 1 && c.Context == "" {
			return "", fmt.Errorf("hunk at patch line %d: the lines match %d places in the file; add more context lines or an '@@ <line>' anchor:\n%s", c.Line, n, strings.Join(old, "\n"))
		}
		reps = append(reps, replacement{idx, len(old), repl})
		pos = idx + len(old)
	}
	for i := len(reps) - 1; i >= 0; i-- {
		r := reps[i]
		tail := append(append([]string{}, r.repl...), lines[r.start+r.n:]...)
		lines = append(lines[:r.start], tail...)
	}
	out := strings.Join(lines, "\n")
	if len(lines) > 0 {
		out += "\n"
	}
	if crlf {
		out = strings.ReplaceAll(out, "\n", "\r\n")
	}
	return out, nil
}

// seekSequence finds pattern in lines at or after start, trying
// progressively looser comparisons: exact, trailing whitespace ignored,
// surrounding whitespace ignored, then Unicode punctuation normalized. It
// returns the first match of the strictest level that matches and how many
// matches that level has, or -1. eof searches from the end of the file first.
func seekSequence(lines, pattern []string, start int, eof bool) (int, int) {
	if len(pattern) == 0 {
		return start, 1
	}
	if len(pattern) > len(lines) {
		return -1, 0
	}
	levels := []func(string) string{
		func(s string) string { return s },
		func(s string) string { return strings.TrimRight(s, " \t") },
		strings.TrimSpace,
		func(s string) string { return normalizePunctuation(strings.TrimSpace(s)) },
	}
	last := len(lines) - len(pattern)
	for _, norm := range levels {
		if eof && last >= start {
			if matchAt(lines, pattern, last, norm) {
				return last, 1
			}
		}
		first, count := -1, 0
		for i := start; i <= last; i++ {
			if matchAt(lines, pattern, i, norm) {
				if first < 0 {
					first = i
				}
				count++
			}
		}
		if first >= 0 {
			return first, count
		}
	}
	return -1, 0
}

func matchAt(lines, pattern []string, at int, norm func(string) string) bool {
	if at < 0 {
		return false
	}
	for j, p := range pattern {
		if norm(lines[at+j]) != norm(p) {
			return false
		}
	}
	return true
}

var punctuation = strings.NewReplacer(
	"\u2010", "-", "\u2011", "-", "\u2012", "-", "\u2013", "-", "\u2014", "-", "\u2015", "-", "\u2212", "-",
	"\u2018", "'", "\u2019", "'", "\u201A", "'", "\u201B", "'",
	"\u201C", `"`, "\u201D", `"`, "\u201E", `"`, "\u201F", `"`,
	"\u00A0", " ", "\u2002", " ", "\u2003", " ", "\u2009", " ", "\u202F", " ", "\u3000", " ",
)

func normalizePunctuation(s string) string { return punctuation.Replace(s) }
