package bench

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ParseReferences reads published per-task grades: either a JSON object
// mapping instance_id to a boolean (eval_results.json), or a CSV with an
// instance_id column, a resolved column (true/false/1/0), and optional steps
// and cost_usd columns.
func ParseReferences(data []byte, harness, model, source string) ([]Reference, error) {
	if harness == "" || model == "" {
		return nil, errors.New("bench: references need a harness and a model")
	}
	trimmed := bytes.TrimSpace(data)
	var refs []Reference
	var err error
	if bytes.HasPrefix(trimmed, []byte("{")) {
		refs, err = parseReferenceJSON(trimmed)
	} else {
		refs, err = parseReferenceCSV(trimmed)
	}
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return nil, errors.New("bench: no references found")
	}
	for i := range refs {
		refs[i].Harness, refs[i].Model, refs[i].Source = harness, model, source
	}
	return refs, nil
}

func parseReferenceJSON(data []byte) ([]Reference, error) {
	var m map[string]bool
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("bench: references JSON must map instance_id to true/false: %w", err)
	}
	refs := make([]Reference, 0, len(m))
	for _, id := range sortedKeys(m) {
		refs = append(refs, Reference{InstanceID: id, Resolved: m[id]})
	}
	return refs, nil
}

func parseReferenceCSV(data []byte) ([]Reference, error) {
	r := csv.NewReader(bytes.NewReader(data))
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("bench: references CSV header: %w", err)
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	idCol, ok1 := col["instance_id"]
	resCol, ok2 := col["resolved"]
	if !ok1 || !ok2 {
		return nil, fmt.Errorf("bench: references CSV needs instance_id and resolved columns, got %v", header)
	}
	var refs []Reference
	for line := 2; ; line++ {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("bench: references CSV line %d: %w", line, err)
		}
		resolved, err := parseBool(rec[resCol])
		if err != nil {
			return nil, fmt.Errorf("bench: references CSV line %d: %w", line, err)
		}
		ref := Reference{InstanceID: strings.TrimSpace(rec[idCol]), Resolved: resolved}
		if i, ok := col["steps"]; ok && strings.TrimSpace(rec[i]) != "" {
			if v, err := strconv.Atoi(strings.TrimSpace(rec[i])); err == nil {
				ref.Steps = &v
			}
		}
		if i, ok := col["cost_usd"]; ok && strings.TrimSpace(rec[i]) != "" {
			if v, err := strconv.ParseFloat(strings.TrimSpace(rec[i]), 64); err == nil {
				ref.CostUSD = &v
			}
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

func parseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "resolved":
		return true, nil
	case "0", "false", "no", "unresolved", "":
		return false, nil
	}
	return false, fmt.Errorf("resolved value %q is not a boolean", s)
}
