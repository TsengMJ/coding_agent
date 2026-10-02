package trace

import (
	"strings"
	"testing"

	"github.com/tsengmingchun/deepagents_golang/llm"
)

func TestRecorderRedactsSecrets(t *testing.T) {
	t.Setenv("DCODE_TEST_DB_PASSWORD", `p"a<ss>word-123`)
	s, r := newRecorder(t)
	output := strings.Join([]string{
		`DB_PASSWORD=p"a<ss>word-123`,
		"sk-or-v1-0123456789abcdef0123456789abcdef",
		"key ctx7sk-00000000-1111-2222-3333-444444444444",
		"postgres://dbench:hunter2secret@localhost:5432/db",
		"risk-assessment-for-the-whole-project stays",
	}, "\n")
	seq, err := r.RecordLLMCall(call(llm.NewUserMessage(output)))
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.LLMCall("thread-1", seq)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"DB_PASSWORD=[REDACTED]",
		"[REDACTED]",
		"key [REDACTED]",
		"postgres://dbench:[REDACTED]@localhost:5432/db",
		"risk-assessment-for-the-whole-project stays",
	}, "\n")
	if got := d.Messages[0].Text(); got != want {
		t.Fatalf("stored message =\n%s\nwant\n%s", got, want)
	}
}
