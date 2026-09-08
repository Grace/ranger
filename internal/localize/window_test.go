package localize

import (
	"fmt"
	"testing"
	"time"

	"github.com/Grace/ranger/internal/trace"
)

// profiles builds a ranked population directly, because the question here is
// about the shape of a whole window rather than about any trace in it.
func profiles(shifts map[string]float64, baseSelf time.Duration) (map[trace.Operation]*Profile, map[trace.Operation]*Profile) {
	base := map[trace.Operation]*Profile{}
	inc := map[trace.Operation]*Profile{}
	for name, ratio := range shifts {
		op := trace.Operation{Service: name, Name: "op"}
		base[op] = &Profile{
			Samples:        100,
			MedianSelfTime: baseSelf,
			MedianDuration: baseSelf,
			MADSelfTime:    baseSelf / 10,
		}
		scaled := time.Duration(float64(baseSelf) * ratio)
		inc[op] = &Profile{
			Samples:        100,
			MedianSelfTime: scaled,
			MedianDuration: scaled,
			MADSelfTime:    baseSelf / 10,
		}
	}
	return base, inc
}

// background returns n operations that did not move, so a window has something
// to be a background.
func background(n int, shift float64) map[string]float64 {
	m := map[string]float64{}
	for i := 0; i < n; i++ {
		m[fmt.Sprintf("svc-%02d", i)] = shift
	}
	return m
}

// A fault is a spike: one operation moves enormously while the rest of the
// system holds still. This is the shape ranking assumes.
func TestASpikeLocalizes(t *testing.T) {
	shifts := background(30, 1.02)
	shifts["culprit"] = 400.0
	base, inc := profiles(shifts, 10*time.Millisecond)

	res := Localize(base, inc, DefaultOptions())
	if !res.Window.Assessed {
		t.Fatalf("30 operations should be enough to describe, got Ops=%d", res.Window.Ops)
	}
	if !res.Localized {
		t.Error("expected a localization")
	}
	if got := res.Candidates[0].Op.Service; got != "culprit" {
		t.Errorf("blamed %q, want culprit", got)
	}
	if res.Window.TopToMedian < 50 {
		t.Errorf("a spike should stand far above its background, got %.1f", res.Window.TopToMedian)
	}
}

// A window where nothing moved is still a window worth describing: it gets a
// median shift near 1.0 and an honest "nothing cleared the bar".
func TestAQuietWindowIsAssessedAndDeclines(t *testing.T) {
	quiet := background(30, 1.01)
	base, inc := profiles(quiet, 10*time.Millisecond)
	res := Localize(base, inc, DefaultOptions())

	if res.Localized {
		t.Fatal("a window where nothing moved should not localize")
	}
	if !res.Window.Assessed {
		t.Error("30 operations is enough to describe a background")
	}
}

// Below the population minimum the statistic is undefined rather than
// permissive: over two operations the median is one of the two, so the fault
// would be its own background. Reporting it as unassessed is the honest answer,
// and it must not interfere with the ranking.
func TestATinyWindowIsNotAssessed(t *testing.T) {
	base, inc := profiles(map[string]float64{"caller": 1.7, "culprit": 270.0}, 10*time.Millisecond)

	res := Localize(base, inc, DefaultOptions())
	if res.Window.Assessed {
		t.Errorf("two operations is not a population, got Ops=%d", res.Window.Ops)
	}
	if !res.Localized {
		t.Error("an unassessable window must not block an otherwise good localization")
	}
}
