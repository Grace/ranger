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
	case r == ByEffect:
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
		c.Verdict, c.Score = classify(c, opt)
		cands = append(cands, c)
	}

	// Deterministic order: score, then depth (deeper wins — a caller's shift
	// is at least partly its callee's), then name.
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].Score != cands[j].Score {
			return cands[i].Score > cands[j].Score
		}
		if cands[i].Depth != cands[j].Depth {
			return cands[i].Depth > cands[j].Depth
		}
		return cands[i].Op.String() < cands[j].Op.String()
	})

	res.Candidates = cands
	res.Localized = len(cands) > 0 && cands[0].Score >= opt.ReportThreshold &&
		cands[0].Verdict != WaitingOnSomethingBelow
	return res
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
	if opt.Rank == ByEffect {
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
