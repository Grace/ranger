package localize

import "time"

// Ranking scores an operation on how far its median self time moved. A median
// is one number out of a whole distribution, and the case this project exists
// to explain is exactly where that throws away the answer.
//
// Manual GC is the example. The pauses do not slow every request a little; they
// stop some requests entirely and leave the rest alone. The distribution goes
// bimodal, the median follows whichever mode holds more than half the samples,
// and an operation can be transformed while its median barely moves.
//
// These two statistics read the samples instead. Neither is used for ranking
// yet, deliberately: the published numbers describe the current ranking, and
// changing the measurement and the ranking in one step would make any
// difference unattributable to either.

// KS is the two-sample Kolmogorov–Smirnov statistic: the largest vertical gap
// between two empirical distributions, in [0, 1].
//
// 0 means the two samples trace the same curve. 1 means they do not overlap at
// all. It is scale free, which is what makes it comparable across a 40µs cache
// hit and a two-second GC pause — the same property `RelativeShift` was
// introduced to get, arrived at without dividing by a baseline that can be zero.
//
// Both inputs must be sorted ascending.
func KS(a, b []time.Duration) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}

	// Walk both sorted samples together, advancing whichever is behind, and
	// track the widest gap between the two running fractions.
	i, j := 0, 0
	na, nb := float64(len(a)), float64(len(b))
	var worst float64

	for i < len(a) && j < len(b) {
		// Advance past every sample at the current value in both, so ties are
		// counted once rather than producing a spurious step.
		v := a[i]
		if b[j] < v {
			v = b[j]
		}
		for i < len(a) && a[i] <= v {
			i++
		}
		for j < len(b) && b[j] <= v {
			j++
		}
		if d := abs(float64(i)/na - float64(j)/nb); d > worst {
			worst = d
		}
	}
	return worst
}

// Wasserstein is the first-order Wasserstein distance — the earth-mover's
// distance — between two samples, returned as a duration.
//
// Where KS asks how far apart the two curves get, this asks how much work it
// would take to turn one into the other, which keeps the units. A result of
// 40ms means the average request moved 40ms, and that is a sentence someone can
// act on in a way that "KS 0.62" is not.
//
// Computed by quantile coupling: match the two samples rank for rank and
// average the gaps. That is exact for equal-length samples and, by resampling
// both onto a common grid of quantiles, close enough for unequal ones.
//
// Both inputs must be sorted ascending.
func Wasserstein(a, b []time.Duration) time.Duration {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}

	// Sample both at the midpoints of n equal-probability bins. Midpoints
	// rather than edges because the 0th and 100th percentiles of a sample are
	// its extremes, and averaging those weights the tails twice.
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	var total float64
	for k := 0; k < n; k++ {
		q := (float64(k) + 0.5) / float64(n)
		total += abs(float64(at(a, q) - at(b, q)))
	}
	return time.Duration(total / float64(n))
}

// at returns the value at quantile q of a sorted slice, by nearest rank.
//
// Nearest rank rather than interpolation, for the reason quantile() in
// aggregate.go gives: an interpolated value is not a duration anything actually
// took, and ranger has to be able to point at its evidence.
func at(sorted []time.Duration, q float64) time.Duration {
	i := int(q * float64(len(sorted)))
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	if i < 0 {
		i = 0
	}
	return sorted[i]
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
