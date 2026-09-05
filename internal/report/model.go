// Package report renders a localization as a single self-contained HTML file.
//
// The visualization is deliberately not a trace waterfall. A waterfall encodes
// duration, and duration is the misleading quantity here: when a leaf slows
// down, every bar above it grows by the same amount and all of them look
// guilty. Every span is drawn split into the work it did itself and the time
// it spent waiting on something below, so the one span that actually got
// slower is the one whose *filled* segment grew. That is the localization
// argument in a form a human can check in one glance.
package report

import (
	"fmt"
	"sort"
	"time"

	"github.com/Grace/inquest/internal/localize"
	"github.com/Grace/inquest/internal/trace"
)

// View is everything the template needs. Built once, rendered once; no logic
// in the template beyond iteration.
type View struct {
	Title      string
	Generated  string
	Localized  bool
	Headline   string
	Subhead    string
	Considered int
	Skipped    int

	Candidates []CandidateView
	Timeline   TimelineView
}

// CandidateView is one row of the ranking, with every number that produced it.
type CandidateView struct {
	Rank    int
	Op      string
	Service string
	Name    string
	Verdict string
	Culprit bool
	Waiting bool

	Score     string
	SelfShift string
	DurShift  string
	Z         string
	ErrShift  string

	BaseSelf string
	IncSelf  string
	BaseDur  string
	IncDur   string
	Samples  string

	// SelfBar and DurBar are percentages of the largest shift in the table,
	// so the two columns are comparable across rows.
	SelfBar float64
	DurBar  float64
}

// TimelineView is one representative trace, drawn as split bars.
type TimelineView struct {
	TraceID  string
	Total    string
	HasTrace bool
	Spans    []TimelineSpan
}

// TimelineSpan is one bar. Offset, Self and Wait are percentages of the root
// span's duration, so the bars line up on a shared axis.
type TimelineSpan struct {
	Service string
	Name    string
	Depth   int
	Indent  int

	Offset float64
	Self   float64
	Wait   float64

	SelfTime string
	Duration string

	Culprit bool
	Waiting bool
	Failed  bool
}

// Build assembles the view from a localization and the incident traces.
func Build(res localize.Result, incident []*trace.Trace, window string) View {
	v := View{
		Title:      "inquest — " + window,
		Generated:  time.Now().UTC().Format("2006-01-02 15:04:05 UTC"),
		Localized:  res.Localized,
		Considered: res.Considered,
		Skipped:    res.Skipped,
	}

	var culprit trace.Operation
	if res.Localized {
		culprit = res.Candidates[0].Op
		top := res.Candidates[0]
		v.Headline = culprit.String()
		v.Subhead = fmt.Sprintf("%s · self time %s → %s (%s)",
			top.Verdict,
			dur(top.Baseline.MedianSelfTime), dur(top.Incident.MedianSelfTime),
			signed(top.SelfTimeShift))
	} else {
		v.Headline = "No operation explains this window"
		v.Subhead = "Nothing cleared the reporting threshold. The ranking below is shown anyway — the near misses are the useful part."
	}

	v.Candidates = candidates(res, culprit)
	v.Timeline = timeline(incident, culprit)
	return v
}

func candidates(res localize.Result, culprit trace.Operation) []CandidateView {
	// Scale both bar columns to the largest shift present, so a 3ms move does
	// not render as dramatically as a 300ms one.
	var maxShift time.Duration
	for _, c := range res.Candidates {
		if c.SelfTimeShift > maxShift {
			maxShift = c.SelfTimeShift
		}
		if c.DurationShift > maxShift {
			maxShift = c.DurationShift
		}
	}

	out := make([]CandidateView, 0, len(res.Candidates))
	for i, c := range res.Candidates {
		cv := CandidateView{
			Rank:      i + 1,
			Op:        c.Op.String(),
			Service:   c.Op.Service,
			Name:      c.Op.Name,
			Verdict:   string(c.Verdict),
			Culprit:   res.Localized && c.Op == culprit,
			Waiting:   c.Verdict == localize.WaitingOnSomethingBelow,
			Score:     fmt.Sprintf("%.1f", c.Score),
			SelfShift: signed(c.SelfTimeShift),
			DurShift:  signed(c.DurationShift),
			Z:         fmt.Sprintf("%.1f", c.SelfTimeZ),
			ErrShift:  fmt.Sprintf("%+.1f pp", 100*c.ErrorRateShift),
			SelfBar:   pct(c.SelfTimeShift, maxShift),
			DurBar:    pct(c.DurationShift, maxShift),
		}
		if c.Baseline != nil {
			cv.BaseSelf = dur(c.Baseline.MedianSelfTime)
			cv.BaseDur = dur(c.Baseline.MedianDuration)
			cv.Samples = fmt.Sprintf("%d / %d", c.Baseline.Samples, c.Incident.Samples)
		} else {
			cv.BaseSelf, cv.BaseDur = "—", "—"
			cv.Samples = fmt.Sprintf("— / %d", c.Incident.Samples)
		}
		cv.IncSelf = dur(c.Incident.MedianSelfTime)
		cv.IncDur = dur(c.Incident.MedianDuration)
		out = append(out, cv)
	}
	return out
}

// timeline picks one trace to draw and lays its spans out on the root's axis.
//
// The trace chosen is the one whose culprit self time is closest to the
// incident median for that operation — a typical example rather than the worst
// one. Showing the worst would flatter the ranking, and this report exists to
// be checked.
func timeline(traces []*trace.Trace, culprit trace.Operation) TimelineView {
	if len(traces) == 0 {
		return TimelineView{}
	}

	var med time.Duration
	var samples []time.Duration
	for _, t := range traces {
		for id, s := range t.Spans {
			if s.Op() == culprit {
				samples = append(samples, t.SelfTime(id))
			}
		}
	}
	if len(samples) > 0 {
		sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
		med = samples[len(samples)/2]
	}

	best := traces[0]
	bestDelta := time.Duration(1<<62 - 1)
	for _, t := range traces {
		for id, s := range t.Spans {
			if s.Op() != culprit {
				continue
			}
			d := t.SelfTime(id) - med
			if d < 0 {
				d = -d
			}
			if d < bestDelta {
				bestDelta, best = d, t
			}
		}
	}

	roots := best.Roots()
	if len(roots) == 0 {
		return TimelineView{}
	}
	root := roots[0]
	total := root.Duration()
	if total <= 0 {
		return TimelineView{}
	}

	tv := TimelineView{TraceID: best.ID, Total: dur(total), HasTrace: true}
	var walk func(s *trace.Span, depth int)
	walk = func(s *trace.Span, depth int) {
		self := best.SelfTime(s.SpanID)
		tv.Spans = append(tv.Spans, TimelineSpan{
			Service:  s.Service,
			Name:     s.Name,
			Depth:    depth,
			Indent:   depth * 14,
			Offset:   pct(s.Start.Sub(root.Start), total),
			Self:     pct(self, total),
			Wait:     pct(s.Duration()-self, total),
			SelfTime: dur(self),
			Duration: dur(s.Duration()),
			Culprit:  s.Op() == culprit,
			// A span that spent almost all its time below itself is a waiter,
			// and is drawn muted so the eye skips it.
			Waiting: s.Duration() > 0 && float64(self)/float64(s.Duration()) < 0.2,
			Failed:  s.Failed(),
		})
		for _, k := range best.Children(s.SpanID) {
			walk(k, depth+1)
		}
	}
	for _, r := range roots {
		walk(r, 0)
	}
	return tv
}

func pct(part, whole time.Duration) float64 {
	if whole <= 0 || part <= 0 {
		return 0
	}
	p := 100 * float64(part) / float64(whole)
	if p > 100 {
		return 100
	}
	return p
}

func dur(d time.Duration) string {
	switch {
	case d == 0:
		return "0"
	case d < time.Microsecond:
		return fmt.Sprintf("%dns", d.Nanoseconds())
	case d < time.Millisecond:
		return fmt.Sprintf("%.0fµs", float64(d.Nanoseconds())/1e3)
	case d < time.Second:
		return fmt.Sprintf("%.1fms", float64(d.Nanoseconds())/1e6)
	default:
		return fmt.Sprintf("%.2fs", d.Seconds())
	}
}

func signed(d time.Duration) string {
	if d >= 0 {
		return "+" + dur(d)
	}
	return "−" + dur(-d)
}
