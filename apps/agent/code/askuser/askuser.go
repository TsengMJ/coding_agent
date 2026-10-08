// Package askuser provides ask_user, a multiple-choice question tool. The
// call pauses the run through the HITL interrupt; the client answers with a
// respond decision whose message becomes the tool result.
package askuser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/middleware/hitl"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

// ToolName is the question tool name.
const ToolName = "ask_user"

const description = "Ask the user one or more multiple-choice questions and wait for the answers. " +
	"Use it only when you are blocked on a decision that is genuinely the user's to make and that you cannot resolve from the request, the code or sensible defaults: an ambiguous requirement, a choice between approaches that change the result significantly, or a missing preference. " +
	"Don't ask about things you can find out yourself by reading the code or running a command.\n\n" +
	"- Ask 1-3 focused questions at a time, each with 2-5 concrete options\n" +
	"- Put the option you recommend first and add \" (Recommended)\" to its label\n" +
	"- The user can always choose \"Other\" and type a free-form answer, so don't add an \"Other\" option yourself\n" +
	"- Set allow_multiple when several options can apply together"

// Option is one answer choice.
type Option struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Question is one multiple-choice question.
type Question struct {
	ID            string   `json:"id"`
	Prompt        string   `json:"prompt"`
	Options       []Option `json:"options"`
	AllowMultiple bool     `json:"allow_multiple,omitempty"`
}

// Answer is the user's reply to one question: chosen option ids and/or
// free text.
type Answer struct {
	QuestionID string   `json:"question_id"`
	OptionIDs  []string `json:"option_ids,omitempty"`
	Other      string   `json:"other,omitempty"`
}

// Middleware contributes ask_user to the main agent only: subagents copy the
// caller's tools but not middleware tools, and they cannot pause the parent
// run to ask anyway.
type Middleware struct{}

func (Middleware) Name() string       { return "AskUserMiddleware" }
func (Middleware) Tools() []tool.Tool { return []tool.Tool{New()} }

// New returns the ask_user tool.
func New() tool.Tool {
	return &tool.Func{
		ToolName:        ToolName,
		ToolDescription: description,
		ToolSchema: tool.Schema{
			"type": "object",
			"properties": map[string]any{
				"questions": map[string]any{
					"type":     "array",
					"minItems": 1,
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":     map[string]any{"type": "string", "description": "Unique id of the question."},
							"prompt": map[string]any{"type": "string", "description": "The question, without the options."},
							"options": map[string]any{
								"type":     "array",
								"minItems": 2,
								"items": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"id":    map[string]any{"type": "string"},
										"label": map[string]any{"type": "string"},
									},
									"required":             []string{"id", "label"},
									"additionalProperties": false,
								},
							},
							"allow_multiple": map[string]any{"type": "boolean", "description": "Whether several options may be chosen."},
						},
						"required":             []string{"id", "prompt", "options"},
						"additionalProperties": false,
					},
				},
			},
			"required":             []string{"questions"},
			"additionalProperties": false,
		},
		Fn: invoke,
	}
}

// invoke only runs when no client paused the call: either the questions are
// malformed (InterruptOn skips those so the model sees why) or nobody can answer.
func invoke(_ context.Context, _ *tool.Runtime, args map[string]any) (*tool.Result, error) {
	if _, err := ParseQuestions(args); err != nil {
		return nil, err
	}
	return nil, errors.New("ask_user needs an interactive client to answer; proceed with your best judgment and state your assumptions")
}

// InterruptOn pauses every well-formed ask_user call, even under
// auto-approve; the client may only respond (answer) or reject (skip).
func InterruptOn() hitl.InterruptOnConfig {
	return hitl.InterruptOnConfig{
		AllowedDecisions: []hitl.DecisionType{hitl.DecisionRespond, hitl.DecisionReject},
		Description:      "The agent has questions for you",
		When: func(call llm.ToolCall) bool {
			_, err := ParseQuestions(call.Args)
			return err == nil
		},
		Always: true,
	}
}

// ParseQuestions decodes and checks the ask_user arguments.
func ParseQuestions(args map[string]any) ([]Question, error) {
	raw, err := json.Marshal(args["questions"])
	if err != nil {
		return nil, fmt.Errorf("questions: %w", err)
	}
	var qs []Question
	if err := json.Unmarshal(raw, &qs); err != nil || len(qs) == 0 {
		return nil, errors.New("questions must be a non-empty list of {id, prompt, options} objects")
	}
	ids := map[string]bool{}
	for i, q := range qs {
		if q.ID == "" || strings.TrimSpace(q.Prompt) == "" {
			return nil, fmt.Errorf("questions[%d] needs an id and a prompt", i)
		}
		if ids[q.ID] {
			return nil, fmt.Errorf("questions[%d].id %q is not unique", i, q.ID)
		}
		ids[q.ID] = true
		if len(q.Options) < 2 {
			return nil, fmt.Errorf("questions[%d] needs at least 2 options", i)
		}
		opts := map[string]bool{}
		for j, o := range q.Options {
			if o.ID == "" || o.Label == "" || opts[o.ID] {
				return nil, fmt.Errorf("questions[%d].options[%d] needs a unique id and a label", i, j)
			}
			opts[o.ID] = true
		}
	}
	return qs, nil
}

// FormatAnswers renders the answers as the tool result the model reads.
func FormatAnswers(qs []Question, answers []Answer) string {
	byID := map[string]Answer{}
	for _, a := range answers {
		byID[a.QuestionID] = a
	}
	var b strings.Builder
	b.WriteString("The user answered:")
	for i, q := range qs {
		a := byID[q.ID]
		var picked []string
		for _, o := range q.Options {
			for _, id := range a.OptionIDs {
				if id == o.ID {
					picked = append(picked, o.Label)
				}
			}
		}
		if other := strings.TrimSpace(a.Other); other != "" {
			picked = append(picked, "Other: "+other)
		}
		answer := strings.Join(picked, "; ")
		if answer == "" {
			answer = "(no answer)"
		}
		fmt.Fprintf(&b, "\n%d. %s\n   -> %s", i+1, q.Prompt, answer)
	}
	return b.String()
}
