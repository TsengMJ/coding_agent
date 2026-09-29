// Package cost estimates token cost per model from pricing tables and local
// prices.json overrides (Python `cost_tracking.py`, see PRICING.md).
package cost

import (
	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
	"github.com/tsengmingchun/deepagents_golang/llm"
)

// Price is per-million-token pricing in USD.
type Price struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
}

// Table maps "provider:model" to prices.
type Table map[string]Price

// LoadTable merges built-in prices with an optional prices.json override.
func LoadTable(overridePath string) (Table, error) {
	return nil, notimpl.Err("cost.LoadTable")
}

// Estimate returns the USD cost of usage for model.
func (t Table) Estimate(model string, usage llm.Usage) (float64, error) {
	return 0, notimpl.Err("cost.Table.Estimate")
}
