package cost

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/tsengmingchun/quill/apps/agent/llm"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestEstimateSplitsCachedInput(t *testing.T) {
	tab := Table{"m": {Input: 2, Output: 8, CacheRead: 0.5}}
	got, err := tab.Estimate("p:m", llm.Usage{InputTokens: 1_000_000, CacheRead: 400_000, OutputTokens: 500_000})
	if err != nil {
		t.Fatal(err)
	}
	// 600k uncached * $2 + 400k cached * $0.5 + 500k out * $8, per million
	if want := 1.2 + 0.2 + 4.0; !near(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestEstimateCacheWithoutPriceUsesInputPrice(t *testing.T) {
	tab := Table{"m": {Input: 1, Output: 1}}
	got, err := tab.Estimate("m", llm.Usage{InputTokens: 1_000_000, CacheRead: 1_000_000})
	if err != nil || !near(got, 1) {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestEstimateUnpricedModel(t *testing.T) {
	_, err := Table{}.Estimate("openai:mystery", llm.Usage{InputTokens: 10})
	if !errors.Is(err, ErrUnpriced) {
		t.Fatalf("err = %v, want ErrUnpriced", err)
	}
}

func TestLookupOrder(t *testing.T) {
	tab := Table{
		"openrouter:gpt-4o": {Input: 9},
		"gpt-4o":            {Input: 2.5},
		"gpt-4o-mini":       {Input: 0.15},
	}
	cases := map[string]float64{
		"openrouter:gpt-4o":        9,
		"openai:gpt-4o":            2.5,
		"openai:gpt-4o-2024-08-06": 2.5,
		"openai:gpt-4o-mini":       0.15,
		"openrouter:openai/gpt-4o": 2.5,
	}
	for model, want := range cases {
		p, ok := tab.Lookup(model)
		if !ok || p.Input != want {
			t.Errorf("%s: got %+v %v, want input %v", model, p, ok, want)
		}
	}
	if _, ok := tab.Lookup("openai:gpt-4oops"); ok {
		t.Error("gpt-4oops must not match gpt-4o (prefix without a '-' boundary)")
	}
}

func TestBuiltinTableHasCommonModels(t *testing.T) {
	tab, err := LoadTable("")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"openai:gpt-4o", "openai:gpt-5", "openrouter:anthropic/claude-sonnet-4"} {
		if _, ok := tab.Lookup(m); !ok {
			t.Errorf("%s not priced", m)
		}
	}
}

func TestLoadTableOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.json")
	body := `{"gpt-4o": {"input": 1, "output": 2}, "openai:gpt-5.5": {"input": 3, "output": 4, "cache_read": 0.3}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tab, err := LoadTable(path)
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := tab.Lookup("openai:gpt-4o"); p.Input != 1 || p.Output != 2 {
		t.Errorf("override not applied: %+v", p)
	}
	if p, ok := tab.Lookup("openai:gpt-5.5"); !ok || p.CacheRead != 0.3 {
		t.Errorf("new model: %+v %v", p, ok)
	}
	if _, ok := tab.Lookup("openai:gpt-4.1"); !ok {
		t.Error("built-in prices dropped")
	}
}

func TestLoadTableMissingOverrideIsFine(t *testing.T) {
	if _, err := LoadTable(filepath.Join(t.TempDir(), "absent.json")); err != nil {
		t.Fatal(err)
	}
}

func TestLoadTableBadOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.json")
	if err := os.WriteFile(path, []byte(`{"m": {"input": "x"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTable(path); err == nil {
		t.Fatal("want error")
	}
}
