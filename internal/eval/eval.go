// Package eval scores inquest against failures somebody else injected.
//
// The distinction this package exists to preserve: naming the wrong service
// and declining to name one are different outcomes, and collapsing them into
// a single "accuracy" number hides the property that matters most at 3am. A
// localizer that is right 60%% of the time and silent the rest is usable. One
// that is right 60%% of the time and confidently wrong the rest is not, and
// both score 60%% if you only count hits.
package eval

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/Grace/inquest/internal/localize"
	"github.com/Grace/inquest/internal/trace"
)

// Case is one labeled incident: two windows, and the service the person who
// injected the failure knows to be responsible.
type Case struct {
	Name string `json:"name"`

	// Flag is the feature flag that was flipped, recorded so the manifest can
	// be checked against the demo's own configuration rather than trusted.
	Flag string `json:"flag"`

	// ExpectService is ground truth: the service whose own work should have
	// changed. Not the service where the symptom was observed.
	ExpectService string `json:"expectService"`

	// ExpectOperation optionally narrows ground truth to one operation. Left
	// empty, any operation belonging to ExpectService counts.
	ExpectOperation string `json:"expectOperation,omitempty"`

	BaselineFile string `json:"baselineFile"`
	IncidentFile string `json:"incidentFile"`

	// Notes records anything that makes the case unusual — a failure that is
	// genuinely ambiguous, a flag whose blast radius covers two services.
	Notes string `json:"notes,omitempty"`
}

// Outcome is what happened on one case.
type Outcome string

const (
	// Correct means the expected service was ranked first.
	Correct Outcome = "correct"

	// InTop3 means it was ranked second or third. Reported separately because
	// a short list a human can scan is a different product from an answer.
	InTop3 Outcome = "in top 3"

	// Wrong means inquest named a different service with confidence. This is
	// the expensive failure and it is counted on its own.
	Wrong Outcome = "wrong"

	// Declined means nothing cleared the reporting threshold. Not a success,
	// but not a lie either.
	Declined Outcome = "declined"
)

// CaseResult is one scored case.
type CaseResult struct {
	Case    Case
	Outcome Outcome

	Named    string // what inquest said, empty when it declined
	Verdict  string
	Rank     int // 1-based rank of the expected service, 0 if absent entirely
	TopScore float64

	Considered int
	Skipped    int
}

// Summary aggregates a run.
type Summary struct {
	Results []CaseResult

	Total    int
	Correct  int
	Top3     int // includes Correct
	Wrong    int
	Declined int
}

// Top1 is the fraction of cases where the responsible service ranked first.
func (s Summary) Top1() float64 { return frac(s.Correct, s.Total) }

// Top3Rate is the fraction where it appeared in the first three.
func (s Summary) Top3Rate() float64 { return frac(s.Top3, s.Total) }

// WrongRate is the fraction where inquest confidently named the wrong service.
// This is the number to lead with when the news is bad.
func (s Summary) WrongRate() float64 { return frac(s.Wrong, s.Total) }

// DeclineRate is the fraction where it said nothing explains this.
func (s Summary) DeclineRate() float64 { return frac(s.Declined, s.Total) }

func frac(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}

// Loader turns a case's two file references into traces. Injected so the
// scoring can be tested without touching a filesystem.
type Loader func(path string) ([]*trace.Trace, error)

// Run scores every case.
func Run(cases []Case, load Loader, opt localize.Options) (Summary, error) {
	var s Summary
	for _, c := range cases {
		baseline, err := load(c.BaselineFile)
		if err != nil {
			return s, fmt.Errorf("%s: baseline: %w", c.Name, err)
		}
		incident, err := load(c.IncidentFile)
		if err != nil {
			return s, fmt.Errorf("%s: incident: %w", c.Name, err)
		}

		res := localize.Localize(
			localize.ProfileWindow(baseline),
			localize.ProfileWindow(incident),
			opt,
		)
		s.Results = append(s.Results, score(c, res))
	}

	s.Total = len(s.Results)
	for _, r := range s.Results {
		switch r.Outcome {
		case Correct:
			s.Correct++
			s.Top3++
		case InTop3:
			s.Top3++
		case Wrong:
			s.Wrong++
		case Declined:
			s.Declined++
		}
	}
	return s, nil
}

func score(c Case, res localize.Result) CaseResult {
	r := CaseResult{Case: c, Considered: res.Considered, Skipped: res.Skipped}
	if len(res.Candidates) > 0 {
		r.TopScore = res.Candidates[0].Score
	}

	// Rank the expected service among candidates inquest was willing to
	// stand behind. An operation labeled "waiting on something below it" is
	// explicitly not an answer, so it does not count as a hit — that is the
	// whole distinction the project claims to make, and letting it score
	// would be marking its own homework.
	rank := 0
	pos := 0
	for _, cand := range res.Candidates {
		if cand.Verdict == localize.WaitingOnSomethingBelow {
			continue
		}
		pos++
		if matches(c, cand.Op) && rank == 0 {
			rank = pos
		}
	}
	r.Rank = rank

	if !res.Localized {
		r.Outcome = Declined
		return r
	}

	top := res.Candidates[0]
	r.Named = top.Op.String()
	r.Verdict = string(top.Verdict)

	switch {
	case rank == 1:
		r.Outcome = Correct
	case rank == 2 || rank == 3:
		r.Outcome = InTop3
	default:
		r.Outcome = Wrong
	}
	return r
}

func matches(c Case, op trace.Operation) bool {
	if op.Service != c.ExpectService {
		return false
	}
	return c.ExpectOperation == "" || op.Name == c.ExpectOperation
}

// ReadManifest loads a case list.
func ReadManifest(r io.Reader) ([]Case, error) {
	var cases []Case
	if err := json.NewDecoder(r).Decode(&cases); err != nil {
		return nil, err
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("manifest contains no cases")
	}
	for i, c := range cases {
		switch {
		case c.Name == "":
			return nil, fmt.Errorf("case %d: name is required", i)
		case c.ExpectService == "":
			return nil, fmt.Errorf("%s: expectService is required — a case with no ground truth cannot be scored", c.Name)
		case c.BaselineFile == "" || c.IncidentFile == "":
			return nil, fmt.Errorf("%s: both baselineFile and incidentFile are required", c.Name)
		}
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].Name < cases[j].Name })
	return cases, nil
}
