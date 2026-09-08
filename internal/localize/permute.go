package localize

import (
	"math/rand"
	"sort"
	"time"

	"github.com/Grace/ranger/internal/trace"
)

// Ranger ranks every operation in a window and then reports the largest score.
// That is a maximum over roughly ninety candidates, and a threshold chosen for
// a single comparison does not hold for it: with enough operations, some
// operation's self time will move far enough to clear any fixed bar for no
// reason at all. The published thresholds — 3.0 deviations, or 1.0 in effect
// terms — were picked by hand and are not calibrated to how many operations
// were searched.
//
// A permutation test answers the question the threshold was standing in for.
// Within each operation, the labels "baseline" and "incident" are exchangeable
// if nothing happened to it. So shuffle them, re-rank the whole window, and
// record the top score. Repeating that traces the distribution of the best
// score obtainable by chance in *this* window with *this* many operations and
// *these* sample counts. The observed top score is then either unusual against
// that distribution or it is not.
//
// The maximum is taken across all operations inside each permutation, which is
// what makes the correction for multiplicity exact rather than approximate:
// the null is the null of the statistic actually reported.
//
// What this does not do is make a wrong answer right. A window in which the
// background moved will still produce a top score that beats its own shuffled
// copies, because the shift is real — it is just not attributable to the
// operation that happens to carry the most of it. Calibration and correctness
// are different problems.

// Significance is the outcome of the permutation test.
type Significance struct {
	// P is the fraction of shuffled windows whose best score was at least the
	// observed one. Small means the top score is hard to obtain by chance.
	P float64

	// Permutations is how many shuffles were run.
	Permutations int

	// NullP95 is the 95th percentile of the shuffled top scores: the score a
	// window like this one reaches by chance one time in twenty. It is the
	// empirical equivalent of the hand-set threshold, and reporting it is what
	// lets a reader see how far off a constant was.
	NullP95 float64

	// Observed is the top score actually seen.
	Observed float64

	// Assessed is false when the window could not be tested — too few
	// operations retained their samples, or none had a baseline.
	Assessed bool
}

// DefaultPermutations balances a stable p against the time someone will wait
// during an incident. At 500 the smallest resolvable p is 0.002, which is far
// finer than any decision made from it.
const DefaultPermutations = 500

// Permute runs the test. Seed is taken explicitly so a reported p is
// reproducible: an unreproducible significance number is not evidence.
func Permute(baseline, incident map[trace.Operation]*Profile, opt Options, permutations int, seed int64) Significance {
	if permutations <= 0 {
		permutations = DefaultPermutations
	}

	// Only operations with both windows and retained samples can be shuffled.
	type pair struct {
		base, inc *Profile
		pool      []time.Duration
		nb        int
		failures  int // pooled across both windows
		total     int
	}
	var pairs []pair
	excluded := make(map[string]bool, len(opt.ExcludeServices))
	for _, s := range opt.ExcludeServices {
		excluded[s] = true
	}
	for op, inc := range incident {
		if excluded[op.Service] {
			continue
		}
		base, ok := baseline[op]
		if !ok || inc.Samples < opt.MinSamples || base.Samples < opt.MinSamples {
			continue
		}
		if len(base.SelfTimes) == 0 || len(inc.SelfTimes) == 0 {
			continue
		}
		pool := make([]time.Duration, 0, len(base.SelfTimes)+len(inc.SelfTimes))
		pool = append(pool, base.SelfTimes...)
		pool = append(pool, inc.SelfTimes...)
		pairs = append(pairs, pair{
			base: base, inc: inc, pool: pool, nb: len(base.SelfTimes),
			failures: base.Failures + inc.Failures,
			total:    len(pool),
		})
	}
	if len(pairs) == 0 {
		return Significance{Permutations: permutations}
	}

	observed := observedTopScore(baseline, incident, opt)

	rng := rand.New(rand.NewSource(seed))
	nulls := make([]float64, permutations)
	buf := make([]time.Duration, 0, 4096)

	for i := 0; i < permutations; i++ {
		best := 0.0
		for _, p := range pairs {
			buf = append(buf[:0], p.pool...)
			rng.Shuffle(len(buf), func(a, b int) { buf[a], buf[b] = buf[b], buf[a] })

			b := append([]time.Duration(nil), buf[:p.nb]...)
			n := append([]time.Duration(nil), buf[p.nb:]...)
			sort.Slice(b, func(i, j int) bool { return b[i] < b[j] })
			sort.Slice(n, func(i, j int) bool { return n[i] < n[j] })

			// Failures are exchanged too. Carrying them through unshuffled
			// makes the test vacuous for error-rate faults: a candidate
			// classified as failing scores on error-rate shift alone, that
			// shift is identical in every permutation, and the null collapses
			// onto the observed value — productCatalogFailure returned p=1.000
			// against its own score of 4.70. Depth and child count stay fixed,
			// because those are properties of the operation's position in the
			// call graph rather than of which window a span landed in.
			fb := hypergeometric(rng, p.failures, p.total, p.nb)
			sb := shuffledProfile(p.base, b, fb)
			si := shuffledProfile(p.inc, n, p.failures-fb)
			if s := scoreOne(p.base.Op, sb, si, opt); s > best {
				best = s
			}
		}
		nulls[i] = best
	}
	sort.Float64s(nulls)

	atLeast := 0
	for _, v := range nulls {
		if v >= observed {
			atLeast++
		}
	}

	return Significance{
		// The +1 in both terms is the standard correction: the observed
		// arrangement is itself one of the possible arrangements, and a
		// reported p of exactly zero would claim more than 500 shuffles can
		// support.
		P:            float64(atLeast+1) / float64(permutations+1),
		Permutations: permutations,
		NullP95:      percentile(nulls, 0.95),
		Observed:     observed,
		Assessed:     true,
	}
}

// shuffledProfile copies a profile with new self-time samples and a new failure
// count, recomputing only the statistics that depend on them.
func shuffledProfile(from *Profile, times []time.Duration, failures int) *Profile {
	med := quantile(times, 0.5)
	p := *from
	p.SelfTimes = times
	p.MedianSelfTime = med
	p.P90SelfTime = quantile(times, 0.9)
	p.MADSelfTime = mad(times, med)
	p.Failures = failures
	if len(times) > 0 {
		p.ErrorRate = float64(failures) / float64(len(times))
	}
	return &p
}

// hypergeometric draws how many of the pooled failures land in a group of size
// draw, sampling without replacement.
//
// Shuffling an explicit slice of booleans would do the same thing and cost a
// second allocation and shuffle per operation per permutation. This is the same
// distribution: pick items one at a time from an urn holding failures successes
// and total-failures others.
func hypergeometric(rng *rand.Rand, failures, total, draw int) int {
	if failures <= 0 || total <= 0 {
		return 0
	}
	if draw >= total {
		return failures
	}
	got, remaining, left := 0, failures, total
	for i := 0; i < draw; i++ {
		if rng.Float64() < float64(remaining)/float64(left) {
			got++
			remaining--
		}
		left--
	}
	return got
}

// scoreOne applies the same classification the ranking uses, so the null is
// the null of the statistic actually reported rather than of a proxy for it.
func scoreOne(op trace.Operation, base, inc *Profile, opt Options) float64 {
	selfShift := inc.MedianSelfTime - base.MedianSelfTime
	c := Candidate{
		Op:             op,
		SelfTimeShift:  selfShift,
		DurationShift:  inc.MedianDuration - base.MedianDuration,
		ErrorRateShift: inc.ErrorRate - base.ErrorRate,
		Baseline:       base,
		Incident:       inc,
		Depth:          inc.MedianDepth,
	}
	c.SelfTimeZ = robustZ(selfShift, base.MADSelfTime, opt.MinSelfTimeShift)
	if base.MedianSelfTime > 0 {
		c.RelativeShift = float64(selfShift) / float64(base.MedianSelfTime)
	}
	_, score := classify(c, opt)
	return score
}

// observedTopScore is the observed maximum, computed through the ordinary path
// so it cannot drift away from what Localize reports.
func observedTopScore(baseline, incident map[trace.Operation]*Profile, opt Options) float64 {
	res := Localize(baseline, incident, opt)
	if len(res.Candidates) == 0 {
		return 0
	}
	return res.Candidates[0].Score
}
