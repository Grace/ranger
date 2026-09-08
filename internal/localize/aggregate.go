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

	"github.com/Grace/ranger/internal/trace"
)

// Sample is one observation of an operation within one trace.
type Sample struct {
	SelfTime time.Duration
	Duration time.Duration
	Depth    int
	Failed   bool

	// Children is how many instrumented children this span had. A span with
	// none has self time equal to its duration by construction, which makes
	// it structurally incapable of being called a waiter no matter what
	// happened underneath it — the work below it simply was not traced.
	Children int
}

// Profile is everything ranger knows about one operation in one window.
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
	// lets ranger say "this operation got slower because something below it
	// did" — a duration shift with no self-time shift underneath it.
	MedianDuration time.Duration

	Failures  int
	ErrorRate float64

	// MedianDepth is how deep this operation usually sits. It breaks ties:
	// when a caller and a callee shift by the same amount, the callee is the
	// better answer, because the caller's shift is at least partly the
	// callee's.
	MedianDepth int

	// MedianChildren is how many instrumented children the operation usually
	// has. Zero means self time carries no information: it equals duration by
	// construction, so the operation always looks like its own cause. Browser
	// page-load timings and load-generator spans are the common case, and
	// they outrank real backend causes because their absolute shifts are
	// larger by orders of magnitude.
	MedianChildren float64

	// SelfTimes is every observation, sorted. The summary above is three
	// numbers out of a distribution, and three numbers cannot answer the
	// questions worth asking of it: whether a shift is larger than chance
	// would produce across this many operations, or whether the shape changed
	// while the median did not. Manual GC is the case in point — the incident
	// distribution is bimodal, and a median moves less than the operation did.
	//
	// The cost is one slice per operation for the life of a comparison, which
	// against ~85 operations of a few hundred samples is nothing next to the
	// traces already in memory.
	SelfTimes []time.Duration
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
				Children: t.ChildCount(id),
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
	kids := make([]int, 0, len(ss))
	failures := 0
	for _, s := range ss {
		times = append(times, s.SelfTime)
		durs = append(durs, s.Duration)
		depths = append(depths, s.Depth)
		kids = append(kids, s.Children)
		if s.Failed {
			failures++
		}
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
	sort.Ints(depths)
	sort.Ints(kids)

	med := quantile(times, 0.5)
	return &Profile{
		Op:             op,
		Samples:        len(ss),
		SelfTimes:      times,
		MedianSelfTime: med,
		P90SelfTime:    quantile(times, 0.9),
		MADSelfTime:    mad(times, med),
		MedianDuration: quantile(durs, 0.5),
		Failures:       failures,
		ErrorRate:      float64(failures) / float64(len(ss)),
		MedianDepth:    depths[len(depths)/2],
		MedianChildren: float64(kids[len(kids)/2]),
	}
}

// quantile returns the q-th quantile of a sorted slice using nearest-rank,
// which keeps the result an observed value rather than an interpolation
// between two of them. An interpolated median is not a duration anything
// actually took, and ranger has to be able to point at its evidence.
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
