// Package trace holds the span model and the one measurement everything else
// in ranger is built on: self time.
//
// A span's duration includes the time its children were running. That makes
// duration useless for localization — when a leaf service slows down, every
// ancestor's duration inflates by the same amount, and the symptom appears
// everywhere at once. Self time is the span's duration minus the wall-clock
// time covered by its children, so it only inflates where work actually got
// slower. "The deepest span whose deviation is not explained by its children"
// is a statement about self time.
package trace

import (
	"sort"
	"time"
)

// Span is the subset of an OpenTelemetry span ranger reasons about.
type Span struct {
	TraceID  string
	SpanID   string
	ParentID string // empty for a root span

	Service string // resource attribute service.name
	Name    string // span name

	Start time.Time
	End   time.Time

	// Status is the OTel status code: "", "OK", or "ERROR". Anything that is
	// not exactly "ERROR" is treated as not-an-error, because unset is the
	// overwhelmingly common case and it does not mean failure.
	Status string

	// Attrs carries the resource and span attributes ranger can key on —
	// service.version and deployment.environment among them. Nil is fine.
	Attrs map[string]string
}

// Duration is the span's own wall-clock span, children included.
func (s Span) Duration() time.Duration { return s.End.Sub(s.Start) }

// Failed reports whether the span carries an ERROR status.
func (s Span) Failed() bool { return s.Status == "ERROR" }

// Operation is the unit ranger localizes to. Two spans are the same operation
// when the same service is doing the same named thing; that is the granularity
// an on-call engineer can act on, and it is coarse enough for the per-window
// sample counts to mean something.
type Operation struct {
	Service string
	Name    string
}

// Op returns the span's operation key.
func (s Span) Op() Operation { return Operation{Service: s.Service, Name: s.Name} }

func (o Operation) String() string { return o.Service + " · " + o.Name }

// Trace is one assembled trace: its spans indexed by span id, with children
// resolved.
type Trace struct {
	ID    string
	Spans map[string]*Span

	children map[string][]*Span
	roots    []*Span
}

// Assemble groups spans into traces and resolves parent/child links.
//
// A span whose parent id is not present in the same trace is treated as a
// root. That happens for real reasons — a sampled-away parent, a trace that
// straddles the query window — and dropping those spans would silently
// discard the part of the trace nearest the client.
func Assemble(spans []Span) []*Trace {
	byTrace := map[string][]*Span{}
	for i := range spans {
		s := &spans[i]
		byTrace[s.TraceID] = append(byTrace[s.TraceID], s)
	}

	out := make([]*Trace, 0, len(byTrace))
	for id, ss := range byTrace {
		t := &Trace{
			ID:       id,
			Spans:    make(map[string]*Span, len(ss)),
			children: map[string][]*Span{},
		}
		for _, s := range ss {
			t.Spans[s.SpanID] = s
		}
		for _, s := range ss {
			if s.ParentID == "" {
				t.roots = append(t.roots, s)
				continue
			}
			if _, ok := t.Spans[s.ParentID]; !ok {
				t.roots = append(t.roots, s)
				continue
			}
			t.children[s.ParentID] = append(t.children[s.ParentID], s)
		}
		sortSpans(t.roots)
		for k := range t.children {
			sortSpans(t.children[k])
		}
		out = append(out, t)
	}

	// Deterministic order. ranger promises the same incident yields the same
	// answer, and that has to survive Go's map iteration.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func sortSpans(ss []*Span) {
	sort.Slice(ss, func(i, j int) bool {
		if !ss[i].Start.Equal(ss[j].Start) {
			return ss[i].Start.Before(ss[j].Start)
		}
		return ss[i].SpanID < ss[j].SpanID
	})
}

// Children returns the direct children of a span, in start order.
func (t *Trace) Children(spanID string) []*Span { return t.children[spanID] }

// ChildCount is how many instrumented children a span has.
//
// It is the caveat that belongs next to every self time. Self time is only
// meaningful relative to what was traced underneath: a span with no children
// has self time equal to its duration whether it did the work itself or spent
// the whole time blocked on something nobody instrumented.
func (t *Trace) ChildCount(spanID string) int { return len(t.children[spanID]) }

// Roots returns the spans with no parent in this trace, in start order.
func (t *Trace) Roots() []*Span { return t.roots }

// Depth is the number of edges from a root to this span. A root has depth 0.
// Depth breaks ties in scoring: when a parent and its child deviate by the
// same amount, the child is the better answer, because the parent's deviation
// is at least partly the child's.
func (t *Trace) Depth(spanID string) int {
	d := 0
	cur := t.Spans[spanID]
	for cur != nil && cur.ParentID != "" {
		next, ok := t.Spans[cur.ParentID]
		if !ok {
			break
		}
		cur = next
		d++
		if d > len(t.Spans) { // cycle guard; malformed input should not hang
			break
		}
	}
	return d
}

// SelfTime is the span's duration minus the wall-clock time covered by its
// children.
//
// Children are merged as intervals rather than summed. Concurrent children —
// a fan-out to three services at once — overlap, and summing their durations
// can exceed the parent's, producing a negative self time that reads as the
// parent getting faster. Merging asks the right question: how much of the
// parent's wall clock was not accounted for by anything below it.
func (t *Trace) SelfTime(spanID string) time.Duration {
	s, ok := t.Spans[spanID]
	if !ok {
		return 0
	}
	kids := t.children[spanID]
	if len(kids) == 0 {
		return s.Duration()
	}

	type iv struct{ start, end time.Time }
	ivs := make([]iv, 0, len(kids))
	for _, k := range kids {
		// Clamp to the parent. A child that reports outside its parent's
		// window is a clock-skew artifact, not the parent doing less work.
		start, end := k.Start, k.End
		if start.Before(s.Start) {
			start = s.Start
		}
		if end.After(s.End) {
			end = s.End
		}
		if !end.After(start) {
			continue
		}
		ivs = append(ivs, iv{start, end})
	}
	sort.Slice(ivs, func(i, j int) bool { return ivs[i].start.Before(ivs[j].start) })

	var covered time.Duration
	var curStart, curEnd time.Time
	for i, v := range ivs {
		if i == 0 {
			curStart, curEnd = v.start, v.end
			continue
		}
		if v.start.After(curEnd) {
			covered += curEnd.Sub(curStart)
			curStart, curEnd = v.start, v.end
			continue
		}
		if v.end.After(curEnd) {
			curEnd = v.end
		}
	}
	if !curEnd.IsZero() {
		covered += curEnd.Sub(curStart)
	}

	self := s.Duration() - covered
	if self < 0 {
		return 0
	}
	return self
}
