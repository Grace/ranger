package localize

import (
	"sort"
	"time"

	"github.com/Grace/ranger/internal/trace"
)

// Verdict says what kind of change an operation underwent, which is the whole
// point of doing this over the trace DAG rather than over a flat set of
// events.
type Verdict string

const (
	// Slower means the operation's own work got slower: its self time shifted.
	// This is a localization — the work is here.
	Slower Verdict = "slower"

	// WaitingOnSomethingBelow means the operation's total duration shifted but
	// its self time did not. It is a victim, not a cause. A dimensional diff
	// cannot tell this apart from the real culprit, because both of them did
	// in fact get slower; only the parent/child structure separates them.
	WaitingOnSomethingBelow Verdict = "waiting on something below it"

	// Failing means the error rate moved materially.
	Failing Verdict = "failing"

	// Appeared means the operation has no baseline at all — a code path that
	// only runs now. Interesting, and not scoreable the same way.
	Appeared Verdict = "appeared"
)

// Candidate is one operation's ranking, with the arithmetic attached. Every
// field here exists so a human can check the ranking rather than trust it.
type Candidate struct {
	Op      trace.Operation
	Verdict Verdict
	Score   float64

	SelfTimeShift  time.Duration
	DurationShift  time.Duration
	SelfTimeZ      float64 // shift measured in baseline MADs
	ErrorRateShift float64

	// RelativeShift is the self-time shift as a fraction of the operation's
	// own baseline: 1.0 means it doubled the work it does itself.
	//
	// SelfTimeZ and this measure different things and the difference decides
	// rankings. A z says how surprising the shift is against that operation's
	// own history; this says how much of a deal it is. They disagree hardest
	// across tiers, where a browser timing moving 30% of a 3-second baseline
	// dwarfs a gRPC handler tripling 3.5ms, and only one of those is a cause.
	RelativeShift float64

	// KS and EarthMover read the whole self-time distribution rather than its
	// median. They are reported, not scored on: the published accuracy
	// describes the current ranking, and moving the measurement and the
	// ranking together would leave any change unattributable to either.
	//
	// They answer different questions and both are kept. KS is scale free and
	// saturates — once two samples are disjoint it reads 1.0 whether they are
	// microseconds or hours apart. EarthMover keeps the units, so it can say
	// how much slower rather than how differently distributed.
	KS         float64
	EarthMover time.Duration

	Baseline *Profile
	Incident *Profile

	Depth int
}

// Ranking selects what an operation's score means.
type Ranking string

const (
	// ByDeviation ranks on robust-z: how far the shift is outside the
	// operation's own historical spread. This is a significance test.
	ByDeviation Ranking = "deviation"

	// ByEffect ranks on the shift as a fraction of the operation's own
	// baseline. This is an effect size, and it is what survives comparison
	// across tiers with wildly different absolute latencies.
	ByEffect Ranking = "effect"

	// ByEffectAdjusted is ByEffect with the window's common movement removed:
	// each operation is scored against how far the *typical* operation moved
	// rather than against zero.
	//
	// A threshold on that common movement was tried first and removed, because
	// four baselines from the same quiet system span 1.51x and any useful bar
	// sits inside ordinary variance. Subtracting is the standard treatment and
	// needs no threshold — if the whole window drifted, every operation carries
	// that drift and dividing it out leaves what is specific to each.
	ByEffectAdjusted Ranking = "effect-adjusted"
)

// Options tunes the thresholds. The defaults are deliberately conservative:
// ranger would rather say nothing explains this than name the wrong service
// at 3am.
type Options struct {
	// MinSamples is the number of observations an operation needs in *both*
	// windows before it can be ranked. Below this, a shift in the median is
	// an artifact of having three data points.
	MinSamples int

	// MinSelfTimeShift is the smallest self-time move worth reporting. Without
	// a floor, an operation whose spread is genuinely near zero produces a
	// huge z from a shift no human would notice.
	MinSelfTimeShift time.Duration

	// MinErrorRateShift is the equivalent floor for failures.
	MinErrorRateShift float64

	// ReportThreshold is the score below which ranger declines to name a
	// cause. This is what makes "no code change explains this" a real answer
	// rather than a thing the README promises.
	ReportThreshold float64

	// Rank selects significance or effect size as the score. Empty means
	// ByDeviation, which is what ranger shipped first.
	Rank Ranking

	// MinWindowOps is how many ranked operations a window needs before drift
	// can be claimed about it. Zero means the default.
	MinWindowOps int

	// ExcludeServices are services removed from the ranking entirely.
	//
	// This exists for instrumentation that is not part of the system being
	// diagnosed — a load generator's own spans are the test harness, and
	// letting them rank means the harness can outscore the service that
	// broke. It is a judgment call and a place to hide a bad number, so
	// Result carries the list back out and every published figure has to say
	// what was excluded. Empty by default: nothing is dropped unless someone
	// says so.
	ExcludeServices []string
}

// waitingShare is how much of an operation's slowdown its own work has to
// account for before that operation can be called a cause. Below this, the
// slowdown came from underneath it.
//
// A fifth is deliberately generous to the caller: an operation genuinely
// getting slower on its own usually accounts for most of its own regression,
// and the cost of being wrong in this direction — hiding a real cause — is
// higher than the cost of showing one extra waiter in the list.
const waitingShare = 0.2

// DefaultOptions are the thresholds used when none are given.
func DefaultOptions() Options {
	return Options{
		MinSamples:        20,
		MinSelfTimeShift:  2 * time.Millisecond,
		MinErrorRateShift: 0.02,
		ReportThreshold:   3.0,
		MinWindowOps:      DefaultMinWindowOps,
	}
}

// ApplyRanking sets the ranking mode and, unless a threshold was chosen
// explicitly, the reporting threshold that goes with it.
//
// The two scores are not in the same units, so one threshold cannot serve
// both. 3.0 deviations is a significance bar. 1.0 in effect terms means the
// operation doubled its own work, which is the point at which "this operation
// got slower" stops being arguable.
func (o Options) ApplyRanking(r Ranking, explicit bool, threshold float64) Options {
	o.Rank = r
	switch {
	case explicit:
		o.ReportThreshold = threshold
	case r == ByEffect, r == ByEffectAdjusted:
		// Same bar for both. Adjusted scores are the same quantity measured
		// against a moving zero, so "the operation doubled its own work
		// relative to what everything else did" is still the claim.
		o.ReportThreshold = 1.0
	}
	return o
}

// Result is a complete localization: the ranking, and whether ranger is
// willing to stand behind the top of it.
type Result struct {
	Candidates []Candidate

	// Excluded is the services dropped before ranking, echoed back so a
	// caller reporting an accuracy number cannot omit them by accident.
	Excluded []string

	// ExcludedOps counts the operations those services accounted for.
	ExcludedOps int

	// Localized is false when nothing cleared ReportThreshold. The candidate
	// list is still returned — an on-call engineer wants to see the near
	// misses — but the answer is "no operation in this window explains it."
	Localized bool

	Considered int // operations that had enough samples to rank
	Skipped    int // operations dropped for thin samples

	// Window carries whether the two windows are comparable at all. A ranking
	// is only a localization if the rest of the system held still while one
	// operation moved.
	Window WindowHealth
}

// WindowHealth describes how much the window moved as a whole.
//
// Ranking assumes a background that did not change. When it did — the load
// shifted, a noisy neighbour woke up, the baseline was captured half an hour
// earlier than the incident — every operation's shift carries that common
// movement, and whichever operation happens to sit on top of the ranking gets
// named as a cause it had nothing to do with. Ranger produced exactly that
// result: `ad · GetAds` topped a window in which nothing had been injected
// into the ad service, because the whole system had drifted 2.5x underneath
// the comparison.
//
// The two shapes are different and the difference is measurable without a
// service graph, without knowing what changed, and without a second baseline:
//
//	a fault  — one operation moves enormously, the median operation does not
//	drift    — the median operation moves too, and the top of the ranking is
//	           barely above it
type WindowHealth struct {
	// MedianShift is the median ratio of incident to baseline self time
	// across every ranked operation. 1.0 means the typical operation did not
	// move. 1.26 means the typical operation got 26% slower, which is a
	// statement about the window rather than about any operation in it.
	MedianShift float64

	// P90Shift is the same ratio at the 90th percentile.
	P90Shift float64

	// TopToMedian is the leading candidate's shift over the median shift. It
	// is the discriminator: a localized fault stands orders of magnitude above
	// its own background, and drift does not.
	TopToMedian float64

	// Ops is how many operations the measurement is over. Below
	// MinWindowOps, Assessed is false: too small a population to say anything
	// about a background.
	Ops int

	// Assessed reports whether the window was large enough to describe.
	Assessed bool
}

// Localize ranks operations by how much of the incident they explain.
func Localize(baseline, incident map[trace.Operation]*Profile, opt Options) Result {
	var res Result
	var cands []Candidate

	excluded := make(map[string]bool, len(opt.ExcludeServices))
	for _, s := range opt.ExcludeServices {
		excluded[s] = true
	}
	res.Excluded = append([]string(nil), opt.ExcludeServices...)
	sort.Strings(res.Excluded)

	for op, inc := range incident {
		if excluded[op.Service] {
			res.ExcludedOps++
			continue
		}
		base, hadBaseline := baseline[op]

		if !hadBaseline {
			if inc.Samples < opt.MinSamples {
				res.Skipped++
				continue
			}
			res.Considered++
			cands = append(cands, Candidate{
				Op:       op,
				Verdict:  Appeared,
				Score:    scoreAppeared(inc),
				Incident: inc,
				Depth:    inc.MedianDepth,
			})
			continue
		}

		if inc.Samples < opt.MinSamples || base.Samples < opt.MinSamples {
			res.Skipped++
			continue
		}
		res.Considered++

		selfShift := inc.MedianSelfTime - base.MedianSelfTime
		durShift := inc.MedianDuration - base.MedianDuration
		errShift := inc.ErrorRate - base.ErrorRate

		c := Candidate{
			Op:             op,
			SelfTimeShift:  selfShift,
			DurationShift:  durShift,
			ErrorRateShift: errShift,
			Baseline:       base,
			Incident:       inc,
			Depth:          inc.MedianDepth,
		}
		c.SelfTimeZ = robustZ(selfShift, base.MADSelfTime, opt.MinSelfTimeShift)
		if base.MedianSelfTime > 0 {
			c.RelativeShift = float64(selfShift) / float64(base.MedianSelfTime)
		}
		c.KS = KS(base.SelfTimes, inc.SelfTimes)
		c.EarthMover = Wasserstein(base.SelfTimes, inc.SelfTimes)
		c.Verdict, c.Score = classify(c, opt)
		cands = append(cands, c)
	}

	sortCandidates(cands)

	res.Window = windowHealth(cands, opt)

	// Adjusted ranking needs the window before it can score, so it is a second
	// pass: the common movement is a property of the population, not of any
	// operation in it.
	if opt.Rank == ByEffectAdjusted {
		reScoreAdjusted(cands, res.Window, opt)
		sortCandidates(cands)
	}

	res.Candidates = cands
	res.Localized = len(cands) > 0 && cands[0].Score >= opt.ReportThreshold &&
		cands[0].Verdict != WaitingOnSomethingBelow
	return res
}

// A threshold on MedianShift was tried here and removed.
//
// The idea was to refuse to localize a window whose background had moved: a
// fault is one operation moving while the rest hold still, drift is everything
// moving together, and 1.20 looked like it separated them. It does not. Four
// baselines captured minutes apart from the same quiet system put ad · GetAds
// at 7.63ms, 8.55ms, 9.08ms and 11.51ms — a 1.51x spread with nothing injected.
// A bar at 1.20 sits inside that, so it was measuring the system breathing, and
// the one window it was built to catch came in at 1.19 and sailed under it.
//
// The numbers below are still worth reporting. What is not available is a
// constant that turns them into a verdict, and the permutation test in
// permute.go answers the question that constant was standing in for.

// DefaultMinWindowOps is the smallest ranked population this check will draw a
// conclusion from.
//
// The question it asks — did the rest of the system hold still? — presumes
// there is a rest of the system. Over two operations there is no background to
// measure and the median is just one of the two, so a single large fault reads
// as a drifting window. That is not a tuning problem, it is the statistic being
// undefined at that size, and the honest response is to decline to judge rather
// than to loosen the threshold until the answer looks right.
const DefaultMinWindowOps = 12

// windowHealth measures how much the ranked population moved as a whole.
//
// It reads the same relative shifts the ranking is built from, so it costs
// nothing extra and cannot disagree with the ranking about what the numbers
// were. Operations that appeared in the incident window with no baseline are
// skipped: they have no ratio, and counting them as an infinite shift would
// let a handful of new operations declare every window drifting.
func windowHealth(cands []Candidate, opt Options) WindowHealth {
	minOps := opt.MinWindowOps
	if minOps <= 0 {
		minOps = DefaultMinWindowOps
	}

	ratios := make([]float64, 0, len(cands))
	for _, c := range cands {
		if c.Baseline == nil || c.Baseline.MedianSelfTime <= 0 {
			continue
		}
		ratios = append(ratios, float64(c.Incident.MedianSelfTime)/float64(c.Baseline.MedianSelfTime))
	}
	if len(ratios) == 0 {
		return WindowHealth{}
	}
	sort.Float64s(ratios)

	h := WindowHealth{
		Ops:         len(ratios),
		MedianShift: median(ratios),
		P90Shift:    percentile(ratios, 0.90),
	}
	if h.MedianShift > 0 {
		h.TopToMedian = ratios[len(ratios)-1] / h.MedianShift
	}
	h.Assessed = len(ratios) >= minOps
	return h
}

// median averages the two middle values on an even-length slice. Nearest-rank
// would return the larger of them, which on a two-operation window makes the
// fault its own background.
func median(sorted []float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// percentile returns the value at q in a sorted slice, without interpolating.
// Nearest-rank is the right choice here: these are a few dozen ratios, and an
// interpolated value between two operations describes no operation.
func percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(float64(len(sorted)) * q)
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}

// sortCandidates puts the ranking in deterministic order: score, then depth
// (deeper wins — a caller's shift is at least partly its callee's), then name.
func sortCandidates(cands []Candidate) {
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].Score != cands[j].Score {
			return cands[i].Score > cands[j].Score
		}
		if cands[i].Depth != cands[j].Depth {
			return cands[i].Depth > cands[j].Depth
		}
		return cands[i].Op.String() < cands[j].Op.String()
	})
}

// reScoreAdjusted divides each operation's shift ratio by the window's median
// ratio, so an operation that merely kept pace with a drifting background
// scores zero rather than scoring the drift.
//
// Only operations already classified as slower are touched. A failing
// operation is scored on error rate, which does not carry the window's latency
// drift, and a waiter is scored zero by construction.
func reScoreAdjusted(cands []Candidate, w WindowHealth, opt Options) {
	// Only deflate a background that got slower. Dividing by a median below
	// 1.0 inflates every operation instead of correcting it, and that is not a
	// hypothetical: adHighCpu's windows came in at 0.81x and 0.86x, and the
	// naive version pushed marginal operations over the threshold in both —
	// converting two honest declines into a wrong answer and a near miss, and
	// inventing a culprit on the control window where nothing was broken.
	//
	// The asymmetry is the point rather than a fudge. Drift that makes the
	// system look worse is what manufactures false positives; a background
	// that sped up cannot, so there is nothing to remove.
	if !w.Assessed || w.MedianShift <= 1 {
		return
	}
	for i := range cands {
		c := &cands[i]
		if c.Verdict != Slower || c.Baseline == nil || c.Baseline.MedianSelfTime <= 0 {
			continue
		}
		// Only rescale what classify already admitted. Writing a score here
		// unconditionally overwrites its absolute floor, and the floor is what
		// stops an operation being promoted by ratio alone — the control
		// window promoted ad · getAdsByCategory at 310µs → 632µs, a clean
		// doubling of a third of a millisecond, on a system where nothing was
		// broken. Adjustment rescales a finding; it does not create one.
		if c.Score <= 0 {
			continue
		}
		ratio := float64(c.Incident.MedianSelfTime) / float64(c.Baseline.MedianSelfTime)
		adjusted := ratio/w.MedianShift - 1
		if adjusted < 0 {
			adjusted = 0
		}
		c.Score = adjusted
	}
}

// classify decides what happened to one operation and how much weight to give
// it.
//
// The ordering matters. An operation that started failing is a better lead
// than one that merely got slower, and an operation that only got slower
// because it is waiting on a child is not a lead at all — it is the symptom
// the caller is looking at.
func classify(c Candidate, opt Options) (Verdict, float64) {
	errMoved := c.ErrorRateShift >= opt.MinErrorRateShift
	selfMoved := c.SelfTimeShift >= opt.MinSelfTimeShift && c.SelfTimeZ > 0

	// Waiting is a question about proportion, not about absolute size: did
	// this operation's own work account for its slowdown, or did something
	// below it?
	//
	// Testing an absolute floor first gets this wrong on real traces. A
	// caller of a service that slowed by 1.9s picks up a few milliseconds of
	// its own noise in the same window; that clears any sane floor and the
	// operation gets called "slower" when its duration moved five hundred
	// times more than its own work did. The demo produced exactly that —
	// frontend's client span for a call into a service in the middle of a
	// GC pause: self +3.6ms, duration +1885.6ms.
	//
	// So the proportion is asked first, and only an operation whose own work
	// explains a real share of its slowdown is a candidate for a cause.
	waiting := c.DurationShift >= opt.MinSelfTimeShift &&
		float64(max(c.SelfTimeShift, 0)) < waitingShare*float64(c.DurationShift)

	// Significance still gates: an operation has to have moved outside its own
	// noise before its effect size means anything. Only the magnitude that
	// gets reported changes.
	mag := c.SelfTimeZ
	if opt.Rank == ByEffect || opt.Rank == ByEffectAdjusted {
		mag = c.RelativeShift
	}

	switch {
	case errMoved && selfMoved && !waiting:
		return Failing, mag + 10*c.ErrorRateShift
	case errMoved && !waiting:
		return Failing, 10 * c.ErrorRateShift
	case waiting:
		// Scored zero and never promoted to an answer. Showing it is still
		// useful: these operations are the path from the symptom down to the
		// cause, which is what an on-call engineer is actually holding.
		return WaitingOnSomethingBelow, 0
	case selfMoved:
		return Slower, mag
	default:
		return Slower, 0
	}
}

// scoreAppeared gives a new operation a score derived from how much of the
// window it accounts for, so a code path that runs once does not outrank a
// real regression.
func scoreAppeared(inc *Profile) float64 {
	s := float64(inc.Samples) / 100
	if inc.ErrorRate > 0 {
		s += 10 * inc.ErrorRate
	}
	if s > 10 {
		s = 10
	}
	return s
}

// robustZ measures a shift in units of the baseline's own spread, with a floor
// under the spread.
//
// The floor is not cosmetic. An operation with a genuinely tiny MAD — a cache
// hit that always takes 40µs — would otherwise produce an enormous z from a
// shift of a few hundred microseconds that no human would ever notice, and it
// would outrank the service that actually fell over.
func robustZ(shift, spread, floor time.Duration) float64 {
	if spread < floor {
		spread = floor
	}
	if spread <= 0 {
		return 0
	}
	return float64(shift) / float64(spread)
}

func max(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
