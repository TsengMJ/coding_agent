package bench

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// fakeCatalog mirrors V2's shape: 3 go repos, 2 python repos, js plus a few
// ts tasks, and a small hard share.
func fakeCatalog() []Task {
	var tasks []Task
	add := func(repo, lang string, n, hard int) {
		for i := range n {
			tasks = append(tasks, Task{InstanceID: fmt.Sprintf("instance_%s_%03d", repo, i), Repo: repo,
				Language: lang, Hard: i < hard})
		}
	}
	add("g1", LangGo, 90, 8)
	add("g2", LangGo, 80, 6)
	add("g3", LangGo, 86, 5)
	add("p1", LangPython, 120, 10)
	add("p2", LangPython, 117, 12)
	add("j1", LangJS, 145, 10)
	add("t1", LangTS, 4, 0)
	return tasks
}

func TestSelectStratifiedQuotas(t *testing.T) {
	tasks := fakeCatalog()
	ids := SelectStratified(tasks, 50, SmokeSeed)
	if len(ids) != 50 {
		t.Fatalf("got %d ids", len(ids))
	}
	byID := map[string]Task{}
	for _, tk := range tasks {
		byID[tk.InstanceID] = tk
	}
	lang := map[string]int{}
	repos := map[string]bool{}
	hard := 0
	for _, id := range ids {
		tk := byID[id]
		lang[tk.Bucket()]++
		repos[tk.Repo] = true
		if tk.Hard {
			hard++
		}
	}
	if lang[LangGo] != 20 || lang[LangPython] != 18 || lang[LangJS] != 12 {
		t.Fatalf("language quotas %v", lang)
	}
	if hard != 4 {
		t.Fatalf("hard %d", hard)
	}
	for _, r := range []string{"g1", "g2", "g3", "p1", "p2", "j1", "t1"} {
		if !repos[r] {
			t.Errorf("repo %s not represented", r)
		}
	}
}

func TestSelectStratifiedIsDeterministic(t *testing.T) {
	a := SelectStratified(fakeCatalog(), 50, 7)
	b := SelectStratified(fakeCatalog(), 50, 7)
	if !slices.Equal(a, b) {
		t.Fatal("same seed gave different subsets")
	}
	if slices.Equal(a, SelectStratified(fakeCatalog(), 50, 8)) {
		t.Fatal("different seeds gave the same subset")
	}
	if !slices.IsSorted(a) {
		t.Fatal("subset is not sorted")
	}
}

func TestLargestRemainder(t *testing.T) {
	got := largestRemainder([]string{"go", "js", "python"}, map[string]int{"go": 256, "js": 149, "python": 237}, 50)
	if got["go"] != 20 || got["js"] != 12 || got["python"] != 18 {
		t.Fatalf("got %v", got)
	}
}

func TestEmbeddedSmokeSubset(t *testing.T) {
	data, err := subsetFS.ReadFile("subsets/smoke50.txt")
	if err != nil {
		t.Fatal(err)
	}
	ids := ReadIDList(string(data))
	if len(ids) != SmokeSize {
		t.Fatalf("smoke50 has %d ids", len(ids))
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("duplicate %s", id)
		}
		seen[id] = true
	}
}

func TestResolveSubset(t *testing.T) {
	tasks := []Task{{InstanceID: "a"}, {InstanceID: "b"}}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hard51_ids.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ids, err := ResolveSubset(SubsetFull, tasks, dir); err != nil || len(ids) != 2 {
		t.Fatalf("full: %v %v", ids, err)
	}
	if ids, err := ResolveSubset(SubsetHard, tasks, dir); err != nil || !slices.Equal(ids, []string{"b"}) {
		t.Fatalf("hard: %v %v", ids, err)
	}
	list := filepath.Join(dir, "mine.txt")
	if err := os.WriteFile(list, []byte("# c\na\nzzz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveSubset(list, tasks, dir); err == nil {
		t.Fatal("unknown id should fail")
	}
	if _, err := ResolveSubset("nope", tasks, dir); err == nil {
		t.Fatal("unknown subset should fail")
	}
}
