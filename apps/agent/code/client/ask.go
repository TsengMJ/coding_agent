package client

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/tsengmingchun/quill/apps/agent/code/askuser"
	"github.com/tsengmingchun/quill/apps/agent/middleware/hitl"
)

// ask shows each ask_user question with numbered options and reads one
// answer line per question.
func (p PromptApprover) ask(req hitl.ActionRequest) (hitl.Decision, error) {
	qs, err := askuser.ParseQuestions(req.Args)
	if err != nil {
		return hitl.Decision{Type: hitl.DecisionReject, Message: err.Error()}, nil
	}
	answers := make([]askuser.Answer, 0, len(qs))
	for i, q := range qs {
		fmt.Fprintf(p.Out, "\n[question %d/%d] %s\n", i+1, len(qs), q.Prompt)
		for j, o := range q.Options {
			fmt.Fprintf(p.Out, "  %d) %s\n", j+1, o.Label)
		}
		hint := "number"
		if q.AllowMultiple {
			hint = "numbers separated by commas"
		}
		fmt.Fprintf(p.Out, "answer (%s, or type your own answer; empty skips): ", hint)
		line, err := p.In.ReadString('\n')
		if err != nil && line == "" {
			return hitl.Decision{}, fmt.Errorf("read answer: %w", err)
		}
		answers = append(answers, ParseAnswerLine(q, line))
	}
	return hitl.Decision{Type: hitl.DecisionRespond, Message: askuser.FormatAnswers(qs, answers)}, nil
}

// ParseAnswerLine reads option numbers ("2" or "1,3") from line; anything
// else is a free-form "Other" answer.
func ParseAnswerLine(q askuser.Question, line string) askuser.Answer {
	a := askuser.Answer{QuestionID: q.ID}
	line = strings.TrimSpace(line)
	if line == "" {
		return a
	}
	var ids []string
	for _, f := range strings.Split(line, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil || n < 1 || n > len(q.Options) {
			a.Other = line
			return a
		}
		ids = append(ids, q.Options[n-1].ID)
	}
	if !q.AllowMultiple {
		ids = ids[:1]
	}
	a.OptionIDs = ids
	return a
}
