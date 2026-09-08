package localize

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/Grace/ranger/internal/trace"
)

// sampled builds a window of operations whose self times are drawn rather than
// asserted, because a permutation test is a statement about samples and cannot
// be exercised by profiles carrying only a median.
func sampled(rng *rand.Rand, ops int, n int, center time.Duration, spread float64) map[trace.Operation]*Profile {
	out := map[trace.Operation]*Profile{}
	for i := 0; i < ops; i++ {
		op := trace.Operation{Service: fmt.Sprintf("svc-%02d", i), Name: "op"}
		times := make([]time.Duration, n)
		for j := range times {
			f := 1 + rng.NormFloat64()*spread
			if f < 0.05 {
				f = 0.05
			}
			times[j] = time.Duration(float64(center) * f)
		}
		out[op] = summarize(op, samplesFrom(times))
	}
	return out
}

func samplesFrom(times []time.Duration) []Sample {
	ss := make([]Sample, len(times))
	for i, t := range times {
		// One instrumented child, so self time carries information: an
		// operation with no children has self time equal to duration by
		// construction and is never a candidate.
		ss[i] = Sample{SelfTime: t, Duration: t * 2, Depth: 3, Children: 1}
	}
	return ss
}

// Under the null — two windows drawn from the same distribution — the test must
// not be able to find a cause. This is the property a hand-set threshold does
// not have: 3.0 deviations means something different over ninety operations
// than it does over five, and nothing in the constant knows which it is facing.
func TestNothingHappenedYieldsALargeP(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	base := sampled(rng, 40, 200, 10*time.Millisecond, 0.30)
	inc := sampled(rng, 40, 200, 10*time.Millisecond, 0.30)

	sig := Permute(base, inc, DefaultOptions(), 300, 1)
	if !sig.Assessed {
		t.Fatal("window should have been assessable")
	}
	if sig.P < 0.05 {
		t.Errorf("two draws from the same distribution should not look significant, got p=%.3f (observed %.2f, null p95 %.2f)",
			sig.P, sig.Observed, sig.NullP95)
	}
}

// And with a real fault it has to fire, or the calibration is useless.
func TestARealFaultYieldsASmallP(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	base := sampled(rng, 40, 200, 10*time.Millisecond, 0.30)
	inc := sampled(rng, 40, 200, 10*time.Millisecond, 0.30)

	// One operation's own work grows by two orders of magnitude, which is the
	// adManualGc shape.
	culprit := trace.Operation{Service: "svc-07", Name: "op"}
	slow := make([]time.Duration, 200)
	for i := range slow {
		slow[i] = time.Duration(float64(2*time.Second) * (1 + rng.NormFloat64()*0.1))
	}
	inc[culprit] = summarize(culprit, samplesFrom(slow))

	sig := Permute(base, inc, DefaultOptions(), 300, 1)
	if sig.P > 0.01 {
		t.Errorf("a 200x fault should be unreachable by chance, got p=%.3f", sig.P)
	}
	if sig.Observed <= sig.NullP95 {
		t.Errorf("observed %.2f should exceed the null p95 %.2f", sig.Observed, sig.NullP95)
	}
}

// The empirical bar is the point: it says what a window of this size reaches by
// chance, which is the thing a constant threshold was guessing at.
//
// Operations are 200ms here rather than 10ms, because that is the regime where
// the question has any content. The 2ms absolute floor already suppresses
// chance findings among small operations — shuffling the labels on a 10ms
// operation almost never moves its median by 2ms, so every shuffled score is
// zero and no threshold, calibrated or otherwise, is doing any work. The floor
// and the permutation test cover different populations, and the interesting one
// is operations whose ordinary spread is large next to the floor.
func TestNullP95GrowsWithThePopulationSearched(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	small := Permute(
		sampled(rng, 6, 120, 200*time.Millisecond, 0.45),
		sampled(rng, 6, 120, 200*time.Millisecond, 0.45),
		DefaultOptions(), 400, 1)
	large := Permute(
		sampled(rng, 90, 120, 200*time.Millisecond, 0.45),
		sampled(rng, 90, 120, 200*time.Millisecond, 0.45),
		DefaultOptions(), 400, 1)

	if small.NullP95 == 0 && large.NullP95 == 0 {
		t.Skip("both nulls degenerate at zero; the absolute floor is doing the work, not the threshold")
	}
	if !(large.NullP95 > small.NullP95) {
		t.Errorf("searching more operations should raise the bar chance can clear: 6 ops p95=%.3f, 90 ops p95=%.3f",
			small.NullP95, large.NullP95)
	}
}

// A reported p that changes between runs is not evidence.
func TestPermutationIsReproducible(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	base := sampled(rng, 30, 150, 10*time.Millisecond, 0.30)
	inc := sampled(rng, 30, 150, 10*time.Millisecond, 0.30)

	a := Permute(base, inc, DefaultOptions(), 200, 42)
	b := Permute(base, inc, DefaultOptions(), 200, 42)
	if a.P != b.P || a.NullP95 != b.NullP95 {
		t.Errorf("same seed gave different answers: %+v vs %+v", a, b)
	}
}
