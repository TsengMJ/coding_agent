// Package cost estimates token cost per model from a built-in price table
// and a local prices.json override (Python `cost_tracking.py`).
package cost

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/tsengmingchun/quill/llm"
)

// ErrUnpriced means the table has no price for the model; callers should
// report the cost as unknown rather than zero.
var ErrUnpriced = errors.New("no price for model")

// Price is per-million-token pricing in USD. A zero CacheRead or CacheWrite
// price bills those tokens at the Input price.
type Price struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read,omitempty"`
	CacheWrite float64 `json:"cache_write,omitempty"`
}

// Table maps a "provider:model" spec or a bare model name to prices.
type Table map[string]Price

//go:embed prices.json
var builtinPrices []byte

// LoadTable returns the built-in prices with overridePath (a prices.json
// in the same format) merged on top. A missing override file is not an error.
func LoadTable(overridePath string) (Table, error) {
	t := Table{}
	if err := json.Unmarshal(builtinPrices, &t); err != nil {
		return nil, fmt.Errorf("cost: built-in prices: %w", err)
	}
	if overridePath == "" {
		return t, nil
	}
	data, err := os.ReadFile(overridePath)
	if errors.Is(err, fs.ErrNotExist) {
		return t, nil
	}
	if err != nil {
		return nil, err
	}
	var override Table
	if err := json.Unmarshal(data, &override); err != nil {
		return nil, fmt.Errorf("cost: %s: %w", overridePath, err)
	}
	for k, p := range override {
		t[k] = p
	}
	return t, nil
}

// Lookup finds the price for a "provider:model" spec: the exact spec, then
// the bare model name (OpenRouter "vendor/" removed), then the longest bare
// key that the name extends with "-" (dated snapshots such as
// gpt-4o-2024-08-06).
func (t Table) Lookup(model string) (Price, bool) {
	if p, ok := t[model]; ok {
		return p, true
	}
	name := model
	if _, rest, ok := strings.Cut(name, ":"); ok {
		name = rest
	}
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	if p, ok := t[name]; ok {
		return p, true
	}
	best := ""
	for k := range t {
		if !strings.Contains(k, ":") && strings.HasPrefix(name, k+"-") && len(k) > len(best) {
			best = k
		}
	}
	if best == "" {
		return Price{}, false
	}
	return t[best], true
}

// Estimate returns the USD cost of usage on model. InputTokens includes
// cached tokens (the OpenAI convention); they are billed at the cache prices.
func (t Table) Estimate(model string, usage llm.Usage) (float64, error) {
	p, ok := t.Lookup(model)
	if !ok {
		return 0, fmt.Errorf("%w %s", ErrUnpriced, model)
	}
	cacheRead, cacheWrite := p.CacheRead, p.CacheWrite
	if cacheRead == 0 {
		cacheRead = p.Input
	}
	if cacheWrite == 0 {
		cacheWrite = p.Input
	}
	uncached := max(usage.InputTokens-usage.CacheRead-usage.CacheWrite, 0)
	total := float64(uncached)*p.Input +
		float64(usage.CacheRead)*cacheRead +
		float64(usage.CacheWrite)*cacheWrite +
		float64(usage.OutputTokens)*p.Output
	return total / 1e6, nil
}
