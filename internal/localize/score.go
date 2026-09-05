package localize

import (
	"sort"
	"time"

	"github.com/Grace/inquest/internal/trace"
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

	Baseline *Profile
	Incident *Profile

	Depth int
}

// Options tunes the thresholds. The defaults are deliberately conservative:
// inquest would rather say nothing explains this than name the wrong service
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

	// ReportThreshold is the score below which inquest declines to name a
	// cause. This is what makes "no code change explains this" a real answer
	// rather than a thing the README promises.
	ReportThreshold float64
}

// DefaultOptions are the thresholds used when none are given.
func DefaultOptions() Options {
	return Options{
		MinSamples:        20,
		MinSelfTimeShift:  2 * time.Millisecond,
		MinErrorRateShift: 0.02,
		ReportThreshold:   3.0,
	}
}

// Result is a complete localization: the ranking, and whether inquest is
// willing to stand behind the top of it.
type Result struct {
	Candidates []Candidate

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

	for op, inc := range incident {
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

	// A duration shift that self time does not account for is downstream.
	// Requiring the duration shift to be materially larger keeps ordinary
	// measurement noise from reclassifying a real local slowdown.
	waiting := c.DurationShift >= opt.MinSelfTimeShift &&
		c.DurationShift > 2*max(c.SelfTimeShift, 0) && !selfMoved

	switch {
	case errMoved && selfMoved:
		return Failing, c.SelfTimeZ + 10*c.ErrorRateShift
	case errMoved:
		return Failing, 10 * c.ErrorRateShift
	case selfMoved:
		return Slower, c.SelfTimeZ
	case waiting:
		// Scored, but never promoted to an answer. Showing it is useful: it
		// is the path from the symptom down to the cause.
		return WaitingOnSomethingBelow, 0
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
