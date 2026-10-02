package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/runtime"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

// DefaultStructuredToolName names the ToolStrategy tool when the schema has
// no usable "title".
const DefaultStructuredToolName = "structured_output"

const defaultStructuredToolDescription = "Return your final answer in this structured format. Call it once, when the task is done; the run ends with it."

var toolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// structuredTool is the synthetic tool behind ToolStrategy: a valid call
// becomes the run's structured response and ends the run, an invalid one is
// returned to the model as an error to correct.
type structuredTool struct {
	name, description string
	schema            tool.Schema
}

// newStructuredTool returns nil when f needs no tool.
func newStructuredTool(f ResponseFormat) (*structuredTool, error) {
	var schema tool.Schema
	switch s := f.(type) {
	case nil:
		return nil, nil
	case ToolStrategy:
		schema = s.Schema
	case AutoStrategy:
		schema = s.Schema
	case ProviderStrategy:
		return nil, notimpl.Err("agent.ProviderStrategy (use ToolStrategy or AutoStrategy)")
	default:
		return nil, fmt.Errorf("agent.CreateAgent: unknown ResponseFormat %T", f)
	}
	if len(schema) == 0 {
		return nil, fmt.Errorf("agent.CreateAgent: ResponseFormat has no schema")
	}
	t := &structuredTool{name: DefaultStructuredToolName, description: defaultStructuredToolDescription, schema: schema}
	if title, _ := schema["title"].(string); toolNamePattern.MatchString(title) {
		t.name = title
	}
	if desc, _ := schema["description"].(string); strings.TrimSpace(desc) != "" {
		t.description = desc
	}
	return t, nil
}

func (t *structuredTool) Name() string        { return t.name }
func (t *structuredTool) Description() string { return t.description }
func (t *structuredTool) Schema() tool.Schema { return t.schema }

func (t *structuredTool) Invoke(_ context.Context, _ *tool.Runtime, args map[string]any) (*tool.Result, error) {
	if problems := validateSchema(t.schema, normalizeJSON(args), "$"); len(problems) > 0 {
		return errorResult(fmt.Sprintf("Error: the structured response does not match the schema: %s. Call `%s` again with corrected arguments.",
			strings.Join(problems, "; "), t.name)), nil
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	return &tool.Result{
		Content: []llm.ContentBlock{llm.TextBlock("Returning structured response: " + string(raw))},
		Update:  &runtime.Update{StructuredResponse: args},
	}, nil
}

// normalizeJSON gives args the shapes encoding/json produces, so numbers
// are float64 whatever the caller built.
func normalizeJSON(v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if json.Unmarshal(raw, &out) != nil {
		return v
	}
	return out
}

// validateSchema checks v against the JSON Schema subset tool schemas use:
// type, enum, properties, required, additionalProperties: false and items.
func validateSchema(schema map[string]any, v any, path string) []string {
	var problems []string
	if typ, ok := schema["type"]; ok && !matchesType(typ, v) {
		return []string{fmt.Sprintf("%s must be %v", path, typ)}
	}
	if enum, ok := schema["enum"].([]any); ok && !slices.ContainsFunc(enum, func(e any) bool { return fmt.Sprint(normalizeJSON(e)) == fmt.Sprint(v) }) {
		problems = append(problems, fmt.Sprintf("%s must be one of %v", path, enum))
	}
	switch val := v.(type) {
	case map[string]any:
		props := schemaMap(schema["properties"])
		for _, name := range stringList(schema["required"]) {
			if _, ok := val[name]; !ok {
				problems = append(problems, fmt.Sprintf("%s.%s is required", path, name))
			}
		}
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if sub := schemaMap(props[k]); sub != nil {
				problems = append(problems, validateSchema(sub, val[k], path+"."+k)...)
			} else if schema["additionalProperties"] == false {
				problems = append(problems, fmt.Sprintf("%s.%s is not allowed", path, k))
			}
		}
	case []any:
		if items := schemaMap(schema["items"]); items != nil {
			for i, item := range val {
				problems = append(problems, validateSchema(items, item, fmt.Sprintf("%s[%d]", path, i))...)
			}
		}
	}
	return problems
}

func matchesType(typ, v any) bool {
	switch list := typ.(type) {
	case []any:
		return slices.ContainsFunc(list, func(t any) bool { return matchesType(t, v) })
	case []string:
		return slices.ContainsFunc(list, func(t string) bool { return matchesType(t, v) })
	}
	switch typ {
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "integer":
		f, ok := v.(float64)
		return ok && f == math.Trunc(f)
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "null":
		return v == nil
	}
	return true
}

func schemaMap(v any) map[string]any {
	switch m := v.(type) {
	case map[string]any:
		return m
	case tool.Schema:
		return m
	}
	return nil
}

func stringList(v any) []string {
	switch l := v.(type) {
	case []string:
		return l
	case []any:
		out := make([]string, 0, len(l))
		for _, s := range l {
			if str, ok := s.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}
