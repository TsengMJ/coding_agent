package commands

import "testing"

func TestCatalogMatchesUpstreamCounts(t *testing.T) {
	public, hidden := 0, 0
	for _, c := range Catalog {
		if c.Hidden {
			hidden++
		} else {
			public++
		}
	}
	if public != 46 || hidden != 2 {
		t.Fatalf("want 46 public / 2 hidden, got %d / %d", public, hidden)
	}
}

func TestCatalogNamesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range Catalog {
		for _, n := range append([]string{c.Name}, c.Aliases...) {
			if seen[n] {
				t.Fatalf("duplicate command name %q", n)
			}
			seen[n] = true
		}
	}
}
