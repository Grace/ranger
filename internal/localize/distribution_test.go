package localize

import (
	"math/rand"
	"sort"
	"testing"
	"time"
)

func ms(v ...int) []time.Duration {
	out := make([]time.Duration, len(v))
	for i, x := range v {
		out[i] = time.Duration(x) * time.Millisecond
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// The value of testing these as pure functions is that the answers are
// checkable by hand.

func TestKSOfIdenticalSamplesIsZero(t *testing.T) {
	a := ms(1, 2, 3, 4, 5)
	if got := KS(a, a); got != 0 {
		t.Errorf("a sample against itself must be 0, got %.4f", got)
	}
}

func TestKSOfDisjointSamplesIsOne(t *testing.T) {
	if got := KS(ms(1, 2, 3), ms(100, 200, 300)); got != 1 {
		t.Errorf("samples that never overlap must be 1, got %.4f", got)
	}
}

// Half the incident samples moved far away and half did not. That is the manual
// GC shape, and it is the case a median misses: the median here has not moved
// at all, while KS reports the half that did.
func TestKSSeesABimodalShiftAMedianMisses(t *testing.T) {
	base := ms(10, 10, 10, 10, 10, 10, 10, 10, 10, 10)
	inc := ms(10, 10, 10, 10, 10, 2000, 2000, 2000, 2000, 2000)

	if quantile(base, 0.5) != quantile(inc, 0.5) {
		t.Fatalf("fixture is wrong: medians should match, got %v and %v",
			quantile(base, 0.5), quantile(inc, 0.5))
	}
	if got := KS(base, inc); got < 0.4 {
		t.Errorf("half the samples moved; KS should be about 0.5, got %.4f", got)
	}
}

func TestKSIsSymmetric(t *testing.T) {
	a, b := ms(1, 5, 9, 12), ms(2, 3, 40, 50)
	if KS(a, b) != KS(b, a) {
		t.Errorf("KS(a,b)=%.4f but KS(b,a)=%.4f", KS(a, b), KS(b, a))
	}
}

// Shifting every sample by a constant moves the earth by that constant, which
// is the property that makes this readable as a duration.
func TestWassersteinOfAConstantShiftIsThatConstant(t *testing.T) {
	base := ms(10, 20, 30, 40, 50)
	inc := ms(60, 70, 80, 90, 100) // every sample +50ms

	got := Wasserstein(base, inc)
	if got < 49*time.Millisecond || got > 51*time.Millisecond {
		t.Errorf("want about 50ms, got %v", got)
	}
}

func TestWassersteinOfIdenticalSamplesIsZero(t *testing.T) {
	a := ms(3, 14, 15, 92, 65)
	if got := Wasserstein(a, a); got != 0 {
		t.Errorf("a sample against itself must be 0, got %v", got)
	}
}

// KS saturates: once two samples are disjoint it reads 1.0 whether they are
// microseconds or hours apart. Wasserstein keeps the magnitude, which is why
// both are carried rather than one.
func TestWassersteinDistinguishesMagnitudesKSCannot(t *testing.T) {
	base := ms(1, 2, 3)
	near := ms(10, 11, 12)
	far := ms(1000, 1100, 1200)

	if KS(base, near) != KS(base, far) {
		t.Fatal("fixture is wrong: both should be disjoint and saturate KS at 1")
	}
	if Wasserstein(base, far) <= Wasserstein(base, near) {
		t.Errorf("the further sample must move more earth: near=%v far=%v",
			Wasserstein(base, near), Wasserstein(base, far))
	}
}

func TestEmptyInputIsZeroRatherThanAPanic(t *testing.T) {
	a := ms(1, 2, 3)
	if KS(a, nil) != 0 || KS(nil, a) != 0 || Wasserstein(a, nil) != 0 {
		t.Error("an empty sample is a window with no data, not a crash")
	}
}

// Unequal sample counts are the normal case — an incident window usually holds
// fewer requests than its baseline, because the requests got slower.
func TestUnequalSampleCountsAreHandled(t *testing.T) {
	rng := rand.New(rand.NewSource(9))
	draw := func(n int, centre time.Duration) []time.Duration {
		out := make([]time.Duration, n)
		for i := range out {
			out[i] = time.Duration(float64(centre) * (1 + rng.NormFloat64()*0.1))
		}
		sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
		return out
	}
	base := draw(300, 10*time.Millisecond)
	inc := draw(40, 10*time.Millisecond)

	if got := KS(base, inc); got > 0.35 {
		t.Errorf("same distribution at different sample sizes should stay small, got %.4f", got)
	}
	if got := Wasserstein(base, inc); got > 2*time.Millisecond {
		t.Errorf("same distribution should move little earth, got %v", got)
	}
}
