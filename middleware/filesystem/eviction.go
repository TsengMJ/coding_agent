package filesystem

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"maps"
	pathpkg "path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/backend"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/runtime"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

// Eviction defaults (Python `_message_eviction.py`).
const (
	DefaultToolTokenLimitBeforeEvict         = 20000
	DefaultHumanMessageTokenLimitBeforeEvict = 50000
	// EvictedToKey tags a user message whose full text was offloaded; the
	// value is the backend path.
	EvictedToKey = "lc_evicted_to"

	charsPerToken          = 4
	previewHeadLines       = 5
	previewTailLines       = 5
	previewLineCharLimit   = 1000
	visibleToolCallIDLimit = 32
)

// toolsExcludedFromEviction already bound their own output size.
var toolsExcludedFromEviction = []string{
	string(ToolLs), string(ToolGlob), string(ToolGrep), string(ToolReadFile),
	string(ToolEditFile), string(ToolWriteFile), string(ToolDelete),
}

const tooLargeToolMsg = `Tool result too large, the result of this tool call %s was saved in the filesystem at this path: %s

You can read the result from the filesystem by using the read_file tool, but make sure to only read part of the result at a time.

You can do this by specifying an offset and limit in the read_file tool call. For example, to read the first 100 lines, you can use the read_file tool with offset=0 and limit=100.

%s

%s
`

const tooLargeHumanMsg = `Message content too large and was saved to the filesystem at: %s

You can read the full content using the read_file tool with pagination (offset and limit parameters).

%s

%s
`

// contentPreview is a line-numbered preview and what was left out of it.
type contentPreview struct {
	text         string
	linesOmitted bool
	linesClipped bool
}

// createContentPreview shows every line when they fit in head+tail lines,
// otherwise the head and tail around a truncation marker. Each shown line
// is clipped to previewLineCharLimit characters.
func createContentPreview(content string) contentPreview {
	lines := strings.Split(strings.TrimSuffix(strings.ReplaceAll(content, "\r\n", "\n"), "\n"), "\n")
	clip := func(shown []string) (string, bool) {
		clipped := false
		out := make([]string, len(shown))
		for i, l := range shown {
			if utf8.RuneCountInString(l) > previewLineCharLimit {
				l, clipped = string([]rune(l)[:previewLineCharLimit]), true
			}
			out[i] = l
		}
		return strings.Join(out, "\n"), clipped
	}
	number := func(s string, start int) string {
		text, _ := backend.FormatContentWithLineNumbers(s, start)
		return text
	}
	if len(lines) <= previewHeadLines+previewTailLines {
		text, clipped := clip(lines)
		return contentPreview{text: number(text, 1), linesClipped: clipped}
	}
	head, headClipped := clip(lines[:previewHeadLines])
	tail, tailClipped := clip(lines[len(lines)-previewTailLines:])
	marker := fmt.Sprintf(backend.TruncationMarkerTemplate, len(lines)-previewHeadLines-previewTailLines)
	return contentPreview{
		text:         number(head, 1) + "\n" + marker + "\n" + number(tail, len(lines)-previewTailLines+1),
		linesOmitted: true,
		linesClipped: headClipped || tailClipped,
	}
}

// note introduces the preview, mentioning only losses it actually has.
func (p contentPreview) note(subject string) string {
	note := "Here is a preview of the " + subject
	if p.linesOmitted {
		note = "Here is a preview showing the head and tail of the " + subject
	}
	var caveats []string
	if p.linesOmitted {
		caveats = append(caveats, fmt.Sprintf("lines of the form `%s` indicate omitted lines in the middle of the content", strings.Replace(backend.TruncationMarkerTemplate, "%d", "N", 1)))
	}
	if p.linesClipped {
		caveats = append(caveats, fmt.Sprintf("the output contains lines longer than %d characters; this preview shows only their first %d characters", previewLineCharLimit, previewLineCharLimit))
	}
	if len(caveats) > 0 {
		note += " (" + strings.Join(caveats, "; ") + ")"
	}
	return note + ":"
}

func visibleToolCallID(id string) string {
	if len(id) > visibleToolCallIDLimit {
		return id[:visibleToolCallIDLimit] + "..."
	}
	return id
}

// textOf joins text blocks; media blocks do not count toward size.
func textOf(blocks []llm.ContentBlock) string {
	var texts []string
	for _, b := range blocks {
		if b.Type == llm.BlockText {
			texts = append(texts, b.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// replaceText swaps every text block for one replacement block, keeping media.
func replaceText(blocks []llm.ContentBlock, text string) []llm.ContentBlock {
	out := []llm.ContentBlock{llm.TextBlock(text)}
	for _, b := range blocks {
		if b.Type != llm.BlockText {
			out = append(out, b)
		}
	}
	return out
}

func limitChars(tokens int) int { return charsPerToken * tokens }

func (m *Middleware) largeToolResultsPrefix() string {
	return pathpkg.Join(backend.ArtifactsRoot(m.opts.Backend), "large_tool_results")
}

func (m *Middleware) conversationHistoryPrefix() string {
	return pathpkg.Join(backend.ArtifactsRoot(m.opts.Backend), "conversation_history")
}

func randomHex(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// write stores content through the backend with a RunContext over state,
// returning the queued files update (StateBackend) on success.
func (m *Middleware) write(ctx context.Context, state *runtime.State, p, content string) (map[string]*backend.FileData, bool) {
	rc := backend.NewRunContext(state)
	if res := m.opts.Backend.Write(backend.WithRunContext(ctx, rc), p, content); res.Error != "" {
		return nil, false
	}
	return rc.FilesUpdate(), true
}

// evictToolResult offloads an oversized tool result to
// {artifactsRoot}/large_tool_results/{id} and replaces its text with a
// preview. When eviction is off, not needed, or the write fails, text
// blocks are clipped to ToolResultTokenLimit instead.
func (m *Middleware) evictToolResult(ctx context.Context, req *agent.ToolCallRequest, res *tool.Result) {
	if res.Command != nil {
		if res.Command.Update != nil {
			m.evictCommandMessages(ctx, req, res.Command.Update)
		}
		return
	}
	id := req.ToolCall.ID
	blocks, files, evicted := m.evictBlocks(ctx, req, id, res.Content)
	res.Content = blocks
	if evicted && files != nil {
		res.Update = mergeFilesUpdate(res.Update, files)
	}
}

// evictCommandMessages applies eviction to tool messages in a Command update.
func (m *Middleware) evictCommandMessages(ctx context.Context, req *agent.ToolCallRequest, upd *runtime.Update) {
	for i, msg := range upd.Messages {
		if msg.Role != llm.RoleTool {
			continue
		}
		blocks, files, evicted := m.evictBlocks(ctx, req, msg.ToolCallID, msg.Content)
		upd.Messages[i].Content = blocks
		if evicted && files != nil {
			mergeFilesUpdate(upd, files)
		}
	}
}

func (m *Middleware) evictBlocks(ctx context.Context, req *agent.ToolCallRequest, id string, blocks []llm.ContentBlock) ([]llm.ContentBlock, map[string]*backend.FileData, bool) {
	limit := m.opts.ToolTokenLimitBeforeEvict
	text := textOf(blocks)
	eligible := limit > 0 && !slices.Contains(toolsExcludedFromEviction, req.ToolCall.Name)
	if eligible && utf8.RuneCountInString(text) > limitChars(limit) {
		name := "unknown-" + randomHex(4)
		if id != "" {
			name = backend.SanitizeToolCallID(id)
		}
		p := m.largeToolResultsPrefix() + "/" + name
		if files, ok := m.write(ctx, req.State, p, text); ok {
			preview := createContentPreview(text)
			stub := fmt.Sprintf(tooLargeToolMsg, visibleToolCallID(id), p, preview.note("result"), preview.text)
			return replaceText(blocks, stub), files, true
		}
	}
	out := slices.Clone(blocks)
	for i, b := range out {
		if b.Type == llm.BlockText {
			out[i].Text = backend.TruncateIfTooLong(b.Text)
		}
	}
	return out, nil, false
}

// mergeFilesUpdate adds files to upd's "files" value, keeping earlier entries.
func mergeFilesUpdate(upd *runtime.Update, files map[string]*backend.FileData) *runtime.Update {
	if upd == nil {
		upd = &runtime.Update{}
	}
	values := maps.Clone(upd.Values)
	if values == nil {
		values = map[string]any{}
	}
	merged := map[string]*backend.FileData{}
	if prev, ok := values[backend.FilesStateKey].(map[string]*backend.FileData); ok {
		maps.Copy(merged, prev)
	}
	maps.Copy(merged, files)
	values[backend.FilesStateKey] = merged
	upd.Values = values
	return upd
}

// evictHumanMessages offloads a new oversized last user message to
// {artifactsRoot}/conversation_history/{uuid}.md and tags it with
// EvictedToKey through the returned state update. Every tagged user
// message is replaced by a preview in the returned request messages; state
// keeps the full text. ok is false when nothing needed processing.
func (m *Middleware) evictHumanMessages(ctx context.Context, req *agent.ModelRequest) (msgs []llm.Message, upd *runtime.Update, ok bool) {
	limit := m.opts.HumanMessageTokenLimitBeforeEvict
	if limit <= 0 || len(req.Messages) == 0 {
		return nil, nil, false
	}
	msgs = slices.Clone(req.Messages)
	last := &msgs[len(msgs)-1]
	if last.Role == llm.RoleUser && evictedTo(*last) == "" && last.ID != "" {
		text := textOf(last.Content)
		if utf8.RuneCountInString(text) > limitChars(limit) {
			p := m.conversationHistoryPrefix() + "/" + newUUID() + ".md"
			if files, written := m.write(ctx, req.State, p, text); written {
				tagged := *last
				tagged.Metadata = maps.Clone(last.Metadata)
				if tagged.Metadata == nil {
					tagged.Metadata = map[string]any{}
				}
				tagged.Metadata[EvictedToKey] = p
				*last = tagged
				upd = &runtime.Update{Messages: []llm.Message{tagged}}
				if files != nil {
					upd = mergeFilesUpdate(upd, files)
				}
			}
		}
	}
	processed := false
	for i, msg := range msgs {
		p := evictedTo(msg)
		if msg.Role != llm.RoleUser || p == "" {
			continue
		}
		text := textOf(msg.Content)
		preview := createContentPreview(text)
		stub := fmt.Sprintf(tooLargeHumanMsg, p, preview.note("content"), preview.text)
		msgs[i].Content = replaceText(msg.Content, stub)
		processed = true
	}
	return msgs, upd, processed || upd != nil
}

func evictedTo(msg llm.Message) string {
	p, _ := msg.Metadata[EvictedToKey].(string)
	return p
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
