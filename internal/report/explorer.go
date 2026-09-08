package report

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/Grace/ranger/internal/localize"
	"github.com/Grace/ranger/internal/trace"
)

// Explorer is the payload the web UI works from. Everything the page can show
// is embedded in the file — no fetches, no server, no CDN. It opens from
// file:// on a laptop with no network, which is the state most people are in
// when they are looking at an incident.
type Explorer struct {
	Window    string     `json:"window"`
	Generated string     `json:"generated"`
	Services  []string   `json:"services"`
	Traces    []ExpTrace `json:"traces"`
	Ranking   []ExpRank  `json:"ranking"`
	Localized bool       `json:"localized"`
	Culprit   string     `json:"culprit"`
	// CauseTraces is how many of the embedded traces actually pass through the
	// localized operation. It is the denominator for "is this window even
	// about the thing ranger named."
	CauseTraces int `json:"causeTraces"`

	// RankBy is the scoring mode the ranking was produced with. The page shows
	// the number it actually sorted by; without this it displayed a robust-z
	// beside a list ordered by effect size, which reads as a sorting bug.
	RankBy string `json:"rankBy"`

	Considered int           `json:"considered"`
	Skipped    int           `json:"skipped"`
	Ops        []ExpOpDetail `json:"ops"`
}

type ExpTrace struct {
	ID       string    `json:"id"`
	Root     string    `json:"root"`
	Service  string    `json:"service"`
	StartMS  float64   `json:"startMs"`
	DurMS    float64   `json:"durMs"`
	Spans    []ExpSpan `json:"spans"`
	Failed   bool      `json:"failed"`
	HasCause bool      `json:"hasCause"`
}

type ExpSpan struct {
	ID       string            `json:"id"`
	Parent   string            `json:"parent"`
	Service  string            `json:"service"`
	Name     string            `json:"name"`
	Depth    int               `json:"depth"`
	OffsetMS float64           `json:"offsetMs"`
	DurMS    float64           `json:"durMs"`
	SelfMS   float64           `json:"selfMs"`
	Failed   bool              `json:"failed"`
	Culprit  bool              `json:"culprit"`
	Attrs    map[string]string `json:"attrs"`
}

type ExpRank struct {
	Op        string  `json:"op"`
	Service   string  `json:"service"`
	Name      string  `json:"name"`
	Verdict   string  `json:"verdict"`
	Score     float64 `json:"score"`
	SelfShift float64 `json:"selfShiftMs"`
	DurShift  float64 `json:"durShiftMs"`
	Z         float64 `json:"z"`
	Rel       float64 `json:"rel"` // self-time shift as a fraction of its own baseline
	Kids      float64 `json:"kids"`
	ErrShift  float64 `json:"errShift"`
	BaseSelf  float64 `json:"baseSelfMs"`
	IncSelf   float64 `json:"incSelfMs"`
	BaseDur   float64 `json:"baseDurMs"`
	IncDur    float64 `json:"incDurMs"`
	BaseN     int     `json:"baseN"`
	IncN      int     `json:"incN"`
	Culprit   bool    `json:"culprit"`
}

// ExpOpDetail carries the two self-time distributions behind one row, so the
// evidence panel can draw them rather than assert a median.
type ExpOpDetail struct {
	Op       string    `json:"op"`
	Baseline []float64 `json:"baseline"`
	Incident []float64 `json:"incident"`
}

// BuildExplorer assembles the payload.
//
// maxTraces caps how many traces are embedded. A demo window can hold tens of
// thousands, and a 200MB HTML file helps nobody; the cap is applied after
// sorting so the traces kept are the slowest, which are the ones anyone opens
// the page to look at.
func BuildExplorer(res localize.Result, baseline, incident []*trace.Trace, window string, maxTraces int, rankBy localize.Ranking) Explorer {
	if rankBy == "" {
		rankBy = localize.ByDeviation
	}
	var culprit trace.Operation
	if res.Localized {
		culprit = res.Candidates[0].Op
	}

	e := Explorer{
		Window:     window,
		Generated:  time.Now().UTC().Format("2006-01-02 15:04:05 UTC"),
		Localized:  res.Localized,
		Considered: res.Considered,
		Skipped:    res.Skipped,
		RankBy:     string(rankBy),
	}
	if res.Localized {
		e.Culprit = culprit.String()
	}

	services := map[string]bool{}
	for _, t := range incident {
		et, ok := expTrace(t, culprit)
		if !ok {
			continue
		}
		for _, s := range et.Spans {
			services[s.Service] = true
		}
		e.Traces = append(e.Traces, et)
	}

	// Traces through the cause first, then slowest.
	//
	// Duration alone is the wrong order and it was actively harmful: the
	// longest traces in a window are long-lived streaming spans — a flagd
	// event stream open for the full ten minutes — which have nothing to do
	// with the incident and are guaranteed to sit at the top. Worse, the cap
	// below is applied after this sort, so ordering by duration could drop
	// every trace containing the cause before anyone saw one.
	//
	// Among traces that do contain the cause, prefer one with a caller over a
	// bare server span. The slowest trace in this window is frequently the
	// cause span on its own, sampled without its parent, and a single bar
	// shows nothing: the claim ranger makes is that the callee's own work
	// moved while the caller merely waited, and that is only legible when
	// both are on screen. Ordering structure ahead of duration makes the
	// first trace anyone opens the one that demonstrates the distinction.
	sort.Slice(e.Traces, func(i, j int) bool {
		if e.Traces[i].HasCause != e.Traces[j].HasCause {
			return e.Traces[i].HasCause
		}
		li, lj := len(e.Traces[i].Spans) > 1, len(e.Traces[j].Spans) > 1
		if li != lj {
			return li
		}
		if e.Traces[i].DurMS != e.Traces[j].DurMS {
			return e.Traces[i].DurMS > e.Traces[j].DurMS
		}
		return e.Traces[i].ID < e.Traces[j].ID
	})
	if maxTraces > 0 && len(e.Traces) > maxTraces {
		e.Traces = e.Traces[:maxTraces]
	}
	for _, t := range e.Traces {
		if t.HasCause {
			e.CauseTraces++
		}
	}

	for s := range services {
		e.Services = append(e.Services, s)
	}
	sort.Strings(e.Services)

	for _, c := range res.Candidates {
		r := ExpRank{
			Op: c.Op.String(), Service: c.Op.Service, Name: c.Op.Name,
			Verdict:   string(c.Verdict),
			Score:     round(c.Score, 2),
			SelfShift: ms(c.SelfTimeShift),
			DurShift:  ms(c.DurationShift),
			Z:         round(c.SelfTimeZ, 2),
			Rel:       round(c.RelativeShift, 3),
			ErrShift:  round(c.ErrorRateShift, 4),
			IncSelf:   ms(c.Incident.MedianSelfTime),
			IncDur:    ms(c.Incident.MedianDuration),
			IncN:      c.Incident.Samples,
			Kids:      c.Incident.MedianChildren,
			Culprit:   res.Localized && c.Op == culprit,
		}
		if c.Baseline != nil {
			r.BaseSelf = ms(c.Baseline.MedianSelfTime)
			r.BaseDur = ms(c.Baseline.MedianDuration)
			r.BaseN = c.Baseline.Samples
		}
		e.Ranking = append(e.Ranking, r)
	}

	e.Ops = opDetails(res, baseline, incident)
	return e
}

// opDetails collects the raw self-time samples for the operations that made
// the top of the ranking, so the page can draw the two distributions instead
// of asking the reader to trust a median.
func opDetails(res localize.Result, baseline, incident []*trace.Trace) []ExpOpDetail {
	const topN = 8
	want := map[trace.Operation]bool{}
	for i, c := range res.Candidates {
		if i >= topN {
			break
		}
		want[c.Op] = true
	}

	collect := func(traces []*trace.Trace) map[trace.Operation][]float64 {
		out := map[trace.Operation][]float64{}
		for _, t := range traces {
			for id, s := range t.Spans {
				op := s.Op()
				if !want[op] {
					continue
				}
				out[op] = append(out[op], ms(t.SelfTime(id)))
			}
		}
		return out
	}
	b, i := collect(baseline), collect(incident)

	var out []ExpOpDetail
	for op := range want {
		bs, is := b[op], i[op]
		sort.Float64s(bs)
		sort.Float64s(is)
		out = append(out, ExpOpDetail{Op: op.String(), Baseline: bs, Incident: is})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Op < out[j].Op })
	return out
}

func expTrace(t *trace.Trace, culprit trace.Operation) (ExpTrace, bool) {
	roots := t.Roots()
	if len(roots) == 0 {
		return ExpTrace{}, false
	}
	root := roots[0]
	total := root.Duration()
	if total <= 0 {
		return ExpTrace{}, false
	}

	et := ExpTrace{
		ID:      t.ID,
		Root:    root.Name,
		Service: root.Service,
		StartMS: float64(root.Start.UnixNano()) / 1e6,
		DurMS:   ms(total),
	}

	var walk func(s *trace.Span, depth int)
	walk = func(s *trace.Span, depth int) {
		self := t.SelfTime(s.SpanID)
		if s.Failed() {
			et.Failed = true
		}
		if s.Op() == culprit {
			et.HasCause = true
		}
		et.Spans = append(et.Spans, ExpSpan{
			ID: s.SpanID, Parent: s.ParentID,
			Service: s.Service, Name: s.Name, Depth: depth,
			OffsetMS: ms(s.Start.Sub(root.Start)),
			DurMS:    ms(s.Duration()),
			SelfMS:   ms(self),
			Failed:   s.Failed(),
			Culprit:  s.Op() == culprit,
			Attrs:    s.Attrs,
		})
		for _, k := range t.Children(s.SpanID) {
			walk(k, depth+1)
		}
	}
	for _, r := range roots {
		walk(r, 0)
	}
	return et, true
}

// JSON returns the payload as compact JSON for embedding in the page.
func (e Explorer) JSON() (string, error) {
	b, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func ms(d time.Duration) float64 { return round(float64(d.Nanoseconds())/1e6, 3) }

func round(f float64, places int) float64 {
	p := 1.0
	for i := 0; i < places; i++ {
		p *= 10
	}
	return float64(int64(f*p+0.5*sign(f))) / p
}

func sign(f float64) float64 {
	if f < 0 {
		return -1
	}
	return 1
}
