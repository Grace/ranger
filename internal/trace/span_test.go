package trace

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

func at(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }

func span(id, parent, service, name string, startMS, endMS int) Span {
	return Span{
		TraceID: "t1", SpanID: id, ParentID: parent,
		Service: service, Name: name,
		Start: at(startMS), End: at(endMS),
	}
}

// The whole localization argument rests on this: a parent that spends all its
// time waiting on one child has no self time of its own.
func TestSelfTimeSubtractsAChild(t *testing.T) {
	tr := Assemble([]Span{
		span("a", "", "frontend", "GET /cart", 0, 100),
		span("b", "a", "cart", "getCart", 10, 90),
	})[0]

	if got, want := tr.SelfTime("a"), 20*time.Millisecond; got != want {
		t.Errorf("parent self time = %v, want %v", got, want)
	}
	if got, want := tr.SelfTime("b"), 80*time.Millisecond; got != want {
		t.Errorf("leaf self time = %v, want %v", got, want)
	}
}

// Concurrent children overlap. Summing their durations would exceed the
// parent's and report the parent as having gone negative — which reads as the
// parent getting faster while the system got slower.
func TestSelfTimeMergesOverlappingChildrenRatherThanSumming(t *testing.T) {
	tr := Assemble([]Span{
		span("a", "", "frontend", "GET /checkout", 0, 100),
		span("b", "a", "cart", "getCart", 10, 80),
		span("c", "a", "shipping", "quote", 20, 90),
	})[0]

	// Children cover 10–90ms as a union: 80ms. Summed, they would be 140ms.
	if got, want := tr.SelfTime("a"), 20*time.Millisecond; got != want {
		t.Errorf("self time = %v, want %v (summing children would give a negative)", got, want)
	}
}

func TestSelfTimeHandlesADisjointGapBetweenChildren(t *testing.T) {
	tr := Assemble([]Span{
		span("a", "", "frontend", "GET /", 0, 100),
		span("b", "a", "cart", "getCart", 0, 30),
		span("c", "a", "ads", "getAds", 60, 100),
	})[0]

	// Covered: 0–30 and 60–100 = 70ms. The 30ms gap is the parent's own work.
	if got, want := tr.SelfTime("a"), 30*time.Millisecond; got != want {
		t.Errorf("self time = %v, want %v", got, want)
	}
}

// Clock skew across hosts is normal and must not manufacture self time.
func TestSelfTimeClampsAChildToItsParentsWindow(t *testing.T) {
	tr := Assemble([]Span{
		span("a", "", "frontend", "GET /", 0, 100),
		span("b", "a", "cart", "getCart", -20, 120),
	})[0]

	if got := tr.SelfTime("a"); got != 0 {
		t.Errorf("self time = %v, want 0 — a child reporting outside its parent is skew, not work", got)
	}
}

func TestAssembleTreatsAnOrphanAsARoot(t *testing.T) {
	// The parent was sampled away or fell outside the query window. Dropping
	// the child would discard the part of the trace nearest the client.
	traces := Assemble([]Span{
		span("b", "missing", "cart", "getCart", 10, 90),
	})
	if len(traces) != 1 {
		t.Fatalf("got %d traces, want 1", len(traces))
	}
	if roots := traces[0].Roots(); len(roots) != 1 || roots[0].SpanID != "b" {
		t.Errorf("roots = %+v, want the orphan promoted to root", roots)
	}
}

func TestDepthCountsEdgesFromTheRoot(t *testing.T) {
	tr := Assemble([]Span{
		span("a", "", "frontend", "GET /", 0, 100),
		span("b", "a", "cart", "getCart", 10, 90),
		span("c", "b", "postgres", "SELECT", 20, 80),
	})[0]

	for id, want := range map[string]int{"a": 0, "b": 1, "c": 2} {
		if got := tr.Depth(id); got != want {
			t.Errorf("depth(%s) = %d, want %d", id, got, want)
		}
	}
}

func TestAssembleIsDeterministic(t *testing.T) {
	// "The same incident yields the same answer" has to survive map iteration.
	in := []Span{
		{TraceID: "t2", SpanID: "x", Start: at(0), End: at(10)},
		{TraceID: "t1", SpanID: "y", Start: at(0), End: at(10)},
		{TraceID: "t3", SpanID: "z", Start: at(0), End: at(10)},
	}
	for i := 0; i < 20; i++ {
		got := Assemble(in)
		if got[0].ID != "t1" || got[1].ID != "t2" || got[2].ID != "t3" {
			t.Fatalf("run %d: trace order = %s %s %s", i, got[0].ID, got[1].ID, got[2].ID)
		}
	}
}
