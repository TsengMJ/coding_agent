package bench

import (
	"embed"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// Subset names accepted by -subset besides the embedded files.
const (
	SubsetFull = "full"
	SubsetHard = "hard"
	// SubsetSmoke is the fixed 50-task subset committed in subsets/smoke50.txt.
	SubsetSmoke = "smoke50"
)

// SmokeSeed and SmokeSize generated subsets/smoke50.txt; the file, not the
// seed, is authoritative.
const (
	SmokeSeed = 20260930
	SmokeSize = 50
)

//go:embed subsets/*.txt
var subsetFS embed.FS

// ResolveSubset returns the instance IDs of a subset: "full" (every task),
// "hard" (v2/hard51_ids.txt), an embedded subsets/<name>.txt, or a path to
// an ID list file. Unknown IDs are an error.
func ResolveSubset(name string, tasks []Task, v2Dir string) ([]string, error) {
	known := map[string]bool{}
	for _, t := range tasks {
		known[t.InstanceID] = true
	}
	var ids []string
	switch name {
	case SubsetFull:
		for _, t := range tasks {
			ids = append(ids, t.InstanceID)
		}
		return ids, nil
	case SubsetHard:
		data, err := os.ReadFile(filepath.Join(v2Dir, "hard51_ids.txt"))
		if err != nil {
			return nil, fmt.Errorf("bench: hard subset: %w", err)
		}
		ids = ReadIDList(string(data))
	default:
		data, err := subsetFS.ReadFile("subsets/" + name + ".txt")
		if err != nil {
			data, err = os.ReadFile(name)
			if err != nil {
				return nil, fmt.Errorf("bench: unknown subset %q (want %s, %s, %s or an ID list file)", name, SubsetSmoke, SubsetHard, SubsetFull)
			}
		}
		ids = ReadIDList(string(data))
	}
	for _, id := range ids {
		if !known[id] {
			return nil, fmt.Errorf("bench: subset %s: unknown task %s", name, id)
		}
	}
	return ids, nil
}

// SelectStratified picks n tasks with a fixed seed. Language buckets (ts
// folded into js) get quotas proportional to their size; hard tasks get a
// proportional share of n spread over buckets by their hard counts; within
// a bucket tasks are taken round-robin across repositories so every repo is
// represented. The result is sorted by instance ID.
func SelectStratified(tasks []Task, n int, seed uint64) []string {
	if n >= len(tasks) {
		n = len(tasks)
	}
	buckets := map[string][]Task{}
	hardCount := map[string]int{}
	totalHard := 0
	for _, t := range tasks {
		b := t.Bucket()
		buckets[b] = append(buckets[b], t)
		if t.Hard {
			hardCount[b]++
			totalHard++
		}
	}
	names := make([]string, 0, len(buckets))
	sizes := map[string]int{}
	for b, ts := range buckets {
		names = append(names, b)
		sizes[b] = len(ts)
	}
	sort.Strings(names)
	quota := largestRemainder(names, sizes, n)
	hardTarget := int(math.Round(float64(n) * float64(totalHard) / float64(len(tasks))))
	hardQuota := largestRemainder(names, hardCount, hardTarget)

	var out []string
	for _, b := range names {
		rng := rand.New(rand.NewPCG(seed, hashString(b)))
		hq := min(hardQuota[b], hardCount[b], quota[b])
		var hard, easy []Task
		for _, t := range buckets[b] {
			if t.Hard {
				hard = append(hard, t)
			} else {
				easy = append(easy, t)
			}
		}
		picked := roundRobin(hard, hq, rng)
		picked = append(picked, roundRobin(easy, quota[b]-len(picked), rng)...)
		out = append(out, picked...)
	}
	slices.Sort(out)
	return out
}

// largestRemainder splits total over keys proportionally to weights
// (Hamilton method); ties go to the key that sorts first.
func largestRemainder(keys []string, weights map[string]int, total int) map[string]int {
	sum := 0
	for _, k := range keys {
		sum += weights[k]
	}
	out := map[string]int{}
	if sum == 0 || total <= 0 {
		return out
	}
	type rem struct {
		key  string
		frac float64
	}
	var rems []rem
	given := 0
	for _, k := range keys {
		exact := float64(total) * float64(weights[k]) / float64(sum)
		out[k] = int(math.Floor(exact))
		given += out[k]
		rems = append(rems, rem{k, exact - math.Floor(exact)})
	}
	sort.SliceStable(rems, func(i, j int) bool { return rems[i].frac > rems[j].frac })
	for i := 0; given < total; i++ {
		out[rems[i%len(rems)].key]++
		given++
	}
	return out
}

// roundRobin shuffles each repository's tasks and takes one per repo in
// repo-name order until n are picked.
func roundRobin(tasks []Task, n int, rng *rand.Rand) []string {
	byRepo := map[string][]string{}
	for _, t := range tasks {
		byRepo[t.Repo] = append(byRepo[t.Repo], t.InstanceID)
	}
	repos := make([]string, 0, len(byRepo))
	for r := range byRepo {
		repos = append(repos, r)
	}
	sort.Strings(repos)
	for _, r := range repos {
		ids := byRepo[r]
		slices.Sort(ids)
		rng.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
	}
	var out []string
	for round := 0; len(out) < n; round++ {
		progressed := false
		for _, r := range repos {
			if len(out) == n {
				break
			}
			if round < len(byRepo[r]) {
				out = append(out, byRepo[r][round])
				progressed = true
			}
		}
		if !progressed {
			break
		}
	}
	return out
}

func hashString(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

// FormatSubset renders an ID list file with a provenance header.
func FormatSubset(ids []string, header ...string) string {
	var b strings.Builder
	for _, h := range header {
		b.WriteString("# " + h + "\n")
	}
	for _, id := range ids {
		b.WriteString(id + "\n")
	}
	return b.String()
}
