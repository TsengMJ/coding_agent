// Package trace records what the agent did in detail so a UI can show it
// later: the full request of every model call (system prompt, messages,
// tools), the diff of every file change, and the messages of subagent runs,
// which never reach thread state.
//
// Each thread is an append-only sequence of JSON records kept in a Log
// (Postgres in dcode). System prompts, messages and tool lists repeat
// across model calls, so each distinct one is stored once as a blob and
// model calls refer to blobs by hash.
package trace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tsengmingchun/deepagents_golang/llm"
)

// ErrNotFound is returned when a model call is not in the trace.
var ErrNotFound = errors.New("trace record not found")

// Log keeps each thread's records, one JSON document per line, in the
// order they were appended.
type Log interface {
	// Append stores lines as one unit: readers see all of them or none, so
	// a record is never read without the blobs it refers to.
	Append(ctx context.Context, threadID string, lines [][]byte) error
	// Each calls fn with every line of threadID in order until fn returns
	// false. An unknown thread has no lines.
	Each(ctx context.Context, threadID string, fn func(line []byte) bool) error
}

// TypedLog is an optional efficient read path for summary views. Implementors
// must not resolve blob bodies when the requested types exclude "blob".
type TypedLog interface {
	EachType(ctx context.Context, threadID string, types []string, fn func(line []byte) bool) error
}

// BlobLog supports direct lookup of only the bodies referenced by one model
// call. PostgreSQL implements it so opening one inspector does not scan every
// blob ever written to the thread.
type BlobLog interface {
	Blobs(ctx context.Context, hashes []string) (map[string]json.RawMessage, error)
}

// responseTextLimit caps the reply text kept in an LLMCall summary.
const responseTextLimit = 400

// LLMCall summarizes one model call.
type LLMCall struct {
	Seq        int       `json:"seq"`
	Namespace  []string  `json:"namespace,omitempty"`
	Model      string    `json:"model,omitempty"`
	Start      time.Time `json:"start"`
	DurationMs int64     `json:"duration_ms"`
	// MessageCount is how many messages were sent, system prompt included.
	MessageCount int `json:"message_count"`
	// NewFrom is the index of the first message that the previous call in
	// the same namespace did not send; earlier messages are unchanged.
	NewFrom   int        `json:"new_from"`
	ToolCount int        `json:"tool_count"`
	Usage     *llm.Usage `json:"usage,omitempty"`
	// ResponseText is the start of the reply text.
	ResponseText string `json:"response_text,omitempty"`
	// ToolCalls names the tools the reply requested.
	ToolCalls []string `json:"tool_calls,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// LLMCallDetail is a model call with its full request and response.
type LLMCallDetail struct {
	LLMCall
	Messages   []llm.Message  `json:"messages"`
	Tools      []llm.ToolSpec `json:"tools,omitempty"`
	ToolChoice string         `json:"tool_choice,omitempty"`
	Settings   map[string]any `json:"settings,omitempty"`
	Response   *llm.Message   `json:"response,omitempty"`
}

// Diff is one file change made by a tool call.
type Diff struct {
	ToolCallID string    `json:"tool_call_id,omitempty"`
	Namespace  []string  `json:"namespace,omitempty"`
	Path       string    `json:"path"`
	Op         string    `json:"op"`
	Unified    string    `json:"unified"`
	Added      int       `json:"added"`
	Removed    int       `json:"removed"`
	Time       time.Time `json:"time"`
}

// Message is a message of a subagent run.
type Message struct {
	Namespace []string    `json:"namespace"`
	Message   llm.Message `json:"message"`
}

// Trace is everything recorded for a thread, without model-call bodies.
type Trace struct {
	ThreadID string    `json:"thread_id"`
	LLMCalls []LLMCall `json:"llm_calls"`
	Diffs    []Diff    `json:"diffs"`
	Messages []Message `json:"messages"`
}

// Update is sent to a recorder's listener as records are written.
type Update struct {
	LLMCall *LLMCall `json:"llm_call,omitempty"`
	Diff    *Diff    `json:"diff,omitempty"`
}

// record is one line of a thread's log.
type record struct {
	Type    string     `json:"type"`
	Blob    *blob      `json:"blob,omitempty"`
	LLM     *llmRecord `json:"llm,omitempty"`
	Diff    *Diff      `json:"diff,omitempty"`
	Message *Message   `json:"message,omitempty"`
}

type blob struct {
	Hash string          `json:"hash"`
	Data json.RawMessage `json:"data"`
}

type llmRecord struct {
	LLMCall
	MessageRefs []string       `json:"message_refs"`
	ToolsRef    string         `json:"tools_ref,omitempty"`
	ToolChoice  string         `json:"tool_choice,omitempty"`
	Settings    map[string]any `json:"settings,omitempty"`
	Response    *llm.Message   `json:"response,omitempty"`
}

// Store reads and writes thread traces in a Log.
type Store struct {
	log    Log
	redact redactor
	mu     sync.Mutex
	recs   map[string]*Recorder
}

// NewStore returns a store on log. Records are written with the values of
// credential-named environment variables, as of now, and well-known token
// formats redacted.
func NewStore(log Log) *Store {
	return &Store{log: log, redact: newRedactor(os.Environ()), recs: map[string]*Recorder{}}
}

// Recorder returns the recorder for threadID, reading its existing records
// on first use so sequence numbers and blob dedup continue.
func (s *Store) Recorder(threadID string) (*Recorder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.recs[threadID]; ok {
		return r, nil
	}
	r := &Recorder{log: s.log, redact: s.redact, threadID: threadID, blobs: map[string]bool{}, last: map[string][]string{}}
	if err := r.scan(); err != nil {
		return nil, err
	}
	s.recs[threadID] = r
	return r, nil
}

// Close forgets the open recorders; the Log's owner closes the Log.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.recs)
	return nil
}

// Load returns the summaries recorded for threadID; an unknown thread has
// an empty trace.
func (s *Store) Load(threadID string) (*Trace, error) {
	t := &Trace{ThreadID: threadID, LLMCalls: []LLMCall{}, Diffs: []Diff{}, Messages: []Message{}}
	err := s.each(threadID, func(rec record) bool {
		switch {
		case rec.LLM != nil:
			t.LLMCalls = append(t.LLMCalls, rec.LLM.LLMCall)
		case rec.Diff != nil:
			t.Diffs = append(t.Diffs, *rec.Diff)
		case rec.Message != nil:
			t.Messages = append(t.Messages, *rec.Message)
		}
		return true
	})
	return t, err
}

// Summary returns model-call and subagent-message metadata without diffs or
// blob bodies. It is the initial GUI trace path; details remain on demand.
func (s *Store) Summary(threadID string) (*Trace, error) {
	t := &Trace{ThreadID: threadID, LLMCalls: []LLMCall{}, Diffs: []Diff{}, Messages: []Message{}}
	err := s.eachTypes(threadID, []string{"llm_call", "message"}, func(rec record) bool {
		if rec.LLM != nil {
			t.LLMCalls = append(t.LLMCalls, rec.LLM.LLMCall)
		} else if rec.Message != nil {
			t.Messages = append(t.Messages, *rec.Message)
		}
		return true
	})
	return t, err
}

// LLMCall returns model call seq of threadID with its full request.
func (s *Store) LLMCall(threadID string, seq int) (*LLMCallDetail, error) {
	var found *llmRecord
	err := s.eachTypes(threadID, []string{"llm_call"}, func(rec record) bool {
		if rec.LLM != nil && rec.LLM.Seq == seq {
			found = rec.LLM
			return false
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, fmt.Errorf("%w: model call %d of thread %s", ErrNotFound, seq, threadID)
	}
	refs := slices.Clone(found.MessageRefs)
	if found.ToolsRef != "" {
		refs = append(refs, found.ToolsRef)
	}
	blobs := map[string]json.RawMessage{}
	if l, ok := s.log.(BlobLog); ok {
		blobs, err = l.Blobs(context.Background(), refs)
	} else {
		err = s.each(threadID, func(rec record) bool {
			if rec.Blob != nil {
				blobs[rec.Blob.Hash] = rec.Blob.Data
			}
			return true
		})
	}
	if err != nil {
		return nil, err
	}
	d := &LLMCallDetail{
		LLMCall: found.LLMCall, ToolChoice: found.ToolChoice, Settings: found.Settings,
		Response: found.Response, Messages: make([]llm.Message, 0, len(found.MessageRefs)),
	}
	for _, h := range found.MessageRefs {
		var m llm.Message
		if err := decodeBlob(blobs, h, &m); err != nil {
			return nil, err
		}
		d.Messages = append(d.Messages, m)
	}
	if found.ToolsRef != "" {
		if err := decodeBlob(blobs, found.ToolsRef, &d.Tools); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// Details returns every model call of threadID with its full request, in
// one pass over the trace (LLMCall rescans the file for each seq).
func (s *Store) Details(threadID string) ([]LLMCallDetail, error) {
	blobs := map[string]json.RawMessage{}
	// Long runs resend the same messages on every call; decoding each blob
	// once keeps memory proportional to the unique messages.
	decoded := map[string]llm.Message{}
	var out []LLMCallDetail
	var decodeErr error
	err := s.each(threadID, func(rec record) bool {
		switch {
		case rec.Blob != nil:
			blobs[rec.Blob.Hash] = rec.Blob.Data
		case rec.LLM != nil:
			d := LLMCallDetail{LLMCall: rec.LLM.LLMCall, ToolChoice: rec.LLM.ToolChoice, Settings: rec.LLM.Settings,
				Response: rec.LLM.Response, Messages: make([]llm.Message, 0, len(rec.LLM.MessageRefs))}
			for _, h := range rec.LLM.MessageRefs {
				m, ok := decoded[h]
				if !ok {
					if decodeErr = decodeBlob(blobs, h, &m); decodeErr != nil {
						return false
					}
					decoded[h] = m
				}
				d.Messages = append(d.Messages, m)
			}
			out = append(out, d)
		}
		return true
	})
	if err == nil {
		err = decodeErr
	}
	return out, err
}

func decodeBlob(blobs map[string]json.RawMessage, hash string, v any) error {
	raw, ok := blobs[hash]
	if !ok {
		return fmt.Errorf("trace: missing blob %s", hash)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("trace: decode blob %s: %w", hash, err)
	}
	return nil
}

// each calls fn for every record of threadID until fn returns false.
func (s *Store) each(threadID string, fn func(record) bool) error {
	return readRecords(s.log, threadID, fn)
}

func (s *Store) eachTypes(threadID string, types []string, fn func(record) bool) error {
	if l, ok := s.log.(TypedLog); ok {
		err := l.EachType(context.Background(), threadID, types, func(line []byte) bool {
			var rec record
			if err := json.Unmarshal(line, &rec); err != nil {
				return true
			}
			return fn(rec)
		})
		if err != nil {
			return fmt.Errorf("trace: read %s: %w", threadID, err)
		}
		return nil
	}
	set := map[string]bool{}
	for _, typ := range types {
		set[typ] = true
	}
	return s.each(threadID, func(rec record) bool { return !set[rec.Type] || fn(rec) })
}

// readRecords decodes threadID's lines; a line that does not decode is
// skipped rather than failing the whole trace.
func readRecords(l Log, threadID string, fn func(record) bool) error {
	err := l.Each(context.Background(), threadID, func(line []byte) bool {
		var rec record
		if err := json.Unmarshal(line, &rec); err != nil {
			return true
		}
		return fn(rec)
	})
	if err != nil {
		return fmt.Errorf("trace: read %s: %w", threadID, err)
	}
	return nil
}

// Recorder appends records to one thread's log.
type Recorder struct {
	mu       sync.Mutex
	log      Log
	redact   redactor
	threadID string
	seq      int
	blobs    map[string]bool
	// last holds the message hashes of the latest call per namespace.
	last     map[string][]string
	listen   func(Update)
	warnOnce sync.Once
}

// Listen sets fn to receive every record written from now on; nil stops.
// A thread runs one run at a time, so there is a single listener.
func (r *Recorder) Listen(fn func(Update)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listen = fn
}

func (r *Recorder) scan() error {
	return readRecords(r.log, r.threadID, func(rec record) bool {
		switch {
		case rec.Blob != nil:
			r.blobs[rec.Blob.Hash] = true
		case rec.LLM != nil:
			r.seq = max(r.seq, rec.LLM.Seq)
			r.last[nsKey(rec.LLM.Namespace)] = rec.LLM.MessageRefs
		}
		return true
	})
}

// LLMCallInput is what RecordLLMCall needs about one model call.
type LLMCallInput struct {
	Namespace []string
	Model     string
	Request   llm.ChatRequest
	Response  *llm.Message
	Start     time.Time
	Duration  time.Duration
	Err       error
}

// RecordLLMCall stores a model call and returns its sequence number.
func (r *Recorder) RecordLLMCall(in LLMCallInput) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var lines [][]byte
	refs := make([]string, len(in.Request.Messages))
	for i, m := range in.Request.Messages {
		h, line, err := r.blob(m)
		if err != nil {
			return 0, err
		}
		refs[i], lines = h, appendLine(lines, line)
	}
	var toolsRef string
	if len(in.Request.Tools) > 0 {
		h, line, err := r.blob(in.Request.Tools)
		if err != nil {
			return 0, err
		}
		toolsRef, lines = h, appendLine(lines, line)
	}
	key := nsKey(in.Namespace)
	r.seq++
	call := LLMCall{
		Seq: r.seq, Namespace: slices.Clone(in.Namespace), Model: in.Model,
		Start: in.Start, DurationMs: in.Duration.Milliseconds(),
		MessageCount: len(refs), NewFrom: commonPrefix(r.last[key], refs), ToolCount: len(in.Request.Tools),
	}
	if in.Err != nil {
		call.Error = in.Err.Error()
	}
	if m := in.Response; m != nil {
		call.Usage = m.Usage
		call.ResponseText = clip(m.Text(), responseTextLimit)
		for _, tc := range m.ToolCalls {
			call.ToolCalls = append(call.ToolCalls, tc.Name)
		}
	}
	line, err := json.Marshal(record{Type: "llm_call", LLM: &llmRecord{
		LLMCall: call, MessageRefs: refs, ToolsRef: toolsRef,
		ToolChoice: in.Request.ToolChoice, Settings: in.Request.Settings, Response: in.Response,
	}})
	if err != nil {
		return 0, fmt.Errorf("trace: encode model call: %w", err)
	}
	if err := r.write(append(lines, line)); err != nil {
		return 0, err
	}
	r.last[key] = refs
	r.notify(Update{LLMCall: &call})
	return call.Seq, nil
}

// RecordDiff stores a file change.
func (r *Recorder) RecordDiff(d Diff) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	line, err := json.Marshal(record{Type: "diff", Diff: &d})
	if err != nil {
		return fmt.Errorf("trace: encode diff: %w", err)
	}
	if err := r.write([][]byte{line}); err != nil {
		return err
	}
	r.notify(Update{Diff: &d})
	return nil
}

// RecordMessage stores a message of a subagent run.
func (r *Recorder) RecordMessage(ns []string, m llm.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	line, err := json.Marshal(record{Type: "message", Message: &Message{Namespace: slices.Clone(ns), Message: m}})
	if err != nil {
		return fmt.Errorf("trace: encode message: %w", err)
	}
	return r.write([][]byte{line})
}

// blob returns the hash of v and, the first time v is seen, its line.
func (r *Recorder) blob(v any) (string, []byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", nil, fmt.Errorf("trace: encode blob: %w", err)
	}
	h := hashBytes(data)
	if r.blobs[h] {
		return h, nil, nil
	}
	line, err := json.Marshal(record{Type: "blob", Blob: &blob{Hash: h, Data: data}})
	if err != nil {
		return "", nil, fmt.Errorf("trace: encode blob: %w", err)
	}
	r.blobs[h] = true
	return h, line, nil
}

// write appends lines as one unit so readers never see a record without
// the blobs it refers to.
func (r *Recorder) write(lines [][]byte) error {
	for i, line := range lines {
		lines[i] = r.redact.line(line)
	}
	if err := r.log.Append(context.Background(), r.threadID, lines); err != nil {
		return fmt.Errorf("trace: write: %w", err)
	}
	return nil
}

// warn reports the first recording failure on stderr; later ones are
// dropped so an unreachable database does not flood the terminal.
func (r *Recorder) warn(err error) {
	r.warnOnce.Do(func() {
		log.Printf("dcode: trace recording failed, details for this thread may be incomplete: %v", err)
	})
}

func (r *Recorder) notify(u Update) {
	if r.listen != nil {
		r.listen(u)
	}
}

func appendLine(lines [][]byte, line []byte) [][]byte {
	if line == nil {
		return lines
	}
	return append(lines, line)
}

func commonPrefix(a, b []string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

func nsKey(ns []string) string { return strings.Join(ns, "/") }

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
