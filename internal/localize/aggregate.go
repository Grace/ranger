// Package localize turns two windows of traces — a baseline and an incident —
// into a ranked list of operations, with the arithmetic that produced the
// ranking attached to each one.
//
// Nothing here consults a model. The ranking is a function of the spans, and
// running it twice on the same input produces the same list in the same order.
package localize

import (
	"math"
	"sort"
	"time"

	"github.com/Grace/inquest/internal/trace"
)

// Sample is one observation of an operation within one trace.
type Sample struct {
	SelfTime time.Duration
	Duration time.Duration
	Depth    int
	Failed   bool
}

// Profile is everything inquest knows about one operation in one window.
type Profile struct {
	Op      trace.Operation
	Samples int

	// MedianSelfTime and P90SelfTime describe the operation's own work.
	// The median is the headline: it is unmoved by the handful of very slow
	// requests present in every window, which is what makes a shift in it
	// mean the typical request got slower rather than the tail got fatter.
	MedianSelfTime time.Duration
	P90SelfTime    time.Duration
	MADSelfTime    time.Duration // median absolute deviation, the spread

	// MedianDuration includes time spent in children. Carrying both is what
	// lets inquest say "this operation got slower because something below it
	// did" — a duration shift with no self-time shift underneath it.
	MedianDuration time.Duration

	Failures  int
	ErrorRate float64

	// MedianDepth is how deep this operation usually sits. It breaks ties:
	// when a caller and a callee shift by the same amount, the callee is the
	// better answer, because the caller's shift is at least partly the
	// callee's.
	MedianDepth int
}

// Profile aggregates traces into one profile per operation.
func ProfileWindow(traces []*trace.Trace) map[trace.Operation]*Profile {
	samples := map[trace.Operation][]Sample{}

	for _, t := range traces {
		for id, s := range t.Spans {
			op := s.Op()
			samples[op] = append(samples[op], Sample{
				SelfTime: t.SelfTime(id),
				Duration: s.Duration(),
				Depth:    t.Depth(id),
				Failed:   s.Failed(),
			})
		}
	}

	out := make(map[trace.Operation]*Profile, len(samples))
	for op, ss := range samples {
		out[op] = summarize(op, ss)
	}
	return out
}

func summarize(op trace.Operation, ss []Sample) *Profile {
	times := make([]time.Duration, 0, len(ss))
	durs := make([]time.Duration, 0, len(ss))
	depths := make([]int, 0, len(ss))
	failures := 0
	for _, s := range ss {
		times = append(times, s.SelfTime)
		durs = append(durs, s.Duration)
		depths = append(depths, s.Depth)
		if s.Failed {
			failures++
		}
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
	sort.Ints(depths)

	med := quantile(times, 0.5)
	return &Profile{
		Op:             op,
		Samples:        len(ss),
		MedianSelfTime: med,
		P90SelfTime:    quantile(times, 0.9),
		MADSelfTime:    mad(times, med),
		MedianDuration: quantile(durs, 0.5),
		Failures:       failures,
		ErrorRate:      float64(failures) / float64(len(ss)),
		MedianDepth:    depths[len(depths)/2],
	}
}

// quantile returns the q-th quantile of a sorted slice using nearest-rank,
// which keeps the result an observed value rather than an interpolation
// between two of them. An interpolated median is not a duration anything
// actually took, and inquest has to be able to point at its evidence.
func quantile(sorted []time.Duration, q float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := int(math.Ceil(q*float64(len(sorted)))) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}

// mad is the median absolute deviation from the median: a spread estimate that
// a few pathological outliers cannot inflate the way they inflate a standard
// deviation. Scoring divides by this, so an estimate that outliers can move is
// an estimate that lets one slow request suppress a real signal.
func mad(sorted []time.Duration, med time.Duration) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	devs := make([]time.Duration, len(sorted))
	for i, v := range sorted {
		d := v - med
		if d < 0 {
			d = -d
		}
		devs[i] = d
	}
	sort.Slice(devs, func(i, j int) bool { return devs[i] < devs[j] })
	return quantile(devs, 0.5)
}
