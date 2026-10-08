package bench

import (
	"math"
	"math/rand/v2"
	"slices"
)

// Wilson returns the 95% Wilson score interval for k successes in n trials.
func Wilson(k, n int) (lo, hi float64) {
	if n == 0 {
		return 0, 0
	}
	const z = 1.959963984540054
	p := float64(k) / float64(n)
	nf := float64(n)
	den := 1 + z*z/nf
	center := (p + z*z/(2*nf)) / den
	half := z * math.Sqrt(p*(1-p)/nf+z*z/(4*nf*nf)) / den
	return max(center-half, 0), min(center+half, 1)
}

// McNemar returns the two-sided exact McNemar p-value for paired binary
// outcomes: onlyA and onlyB count the tasks only one side resolved.
func McNemar(onlyA, onlyB int) float64 {
	n := onlyA + onlyB
	if n == 0 {
		return 1
	}
	k := min(onlyA, onlyB)
	// P(X <= k) for X ~ Binomial(n, 1/2), computed in log space.
	var tail float64
	for i := 0; i <= k; i++ {
		tail += math.Exp(logChoose(n, i) - float64(n)*math.Ln2)
	}
	return min(2*tail, 1)
}

func logChoose(n, k int) float64 {
	a, _ := math.Lgamma(float64(n + 1))
	b, _ := math.Lgamma(float64(k + 1))
	c, _ := math.Lgamma(float64(n - k + 1))
	return a - b - c
}

// BootstrapDiff returns the 95% percentile interval of mean(b) - mean(a)
// over paired per-task scores, resampling tasks with a fixed seed.
func BootstrapDiff(a, b []float64, iters int, seed uint64) (lo, hi float64) {
	n := len(a)
	if n == 0 || n != len(b) {
		return 0, 0
	}
	rng := rand.New(rand.NewPCG(seed, 0x9e3779b97f4a7c15))
	diffs := make([]float64, iters)
	for it := range diffs {
		var s float64
		for range n {
			i := rng.IntN(n)
			s += b[i] - a[i]
		}
		diffs[it] = s / float64(n)
	}
	slices.Sort(diffs)
	return quantile(diffs, 0.025), quantile(diffs, 0.975)
}

// quantile interpolates the q-quantile of sorted values.
func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	pos := q * float64(len(sorted)-1)
	i := int(pos)
	if i+1 >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	frac := pos - float64(i)
	return sorted[i]*(1-frac) + sorted[i+1]*frac
}

// Median returns the median of values (0 when empty).
func Median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	s := slices.Clone(values)
	slices.Sort(s)
	return quantile(s, 0.5)
}
