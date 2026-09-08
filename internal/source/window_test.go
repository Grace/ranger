package source

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// streamOf writes spans as the collector's file exporter does: one
// resourceSpans batch per line, in arrival order.
func streamOf(starts []time.Time) string {
	var b strings.Builder
	for i, t := range starts {
		line := map[string]any{
			"resourceSpans": []any{map[string]any{
				"resource": map[string]any{"attributes": []any{
					map[string]any{"key": "service.name", "value": map[string]any{"stringValue": "svc"}},
				}},
				"scopeSpans": []any{map[string]any{"spans": []any{
					map[string]any{
						"traceId":           fmt.Sprintf("%032x", i),
						"spanId":            fmt.Sprintf("%016x", i),
						"name":              "op",
						"startTimeUnixNano": fmt.Sprint(t.UnixNano()),
						"endTimeUnixNano":   fmt.Sprint(t.Add(time.Millisecond).UnixNano()),
					},
				}}},
			}},
		}
		j, _ := json.Marshal(line)
		b.Write(j)
		b.WriteByte('\n')
	}
	return b.String()
}

func readerOver(s string) StreamReader {
	return StreamReader{Open: func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(s)), nil
	}}
}

func TestSpansAreSelectedByStartTime(t *testing.T) {
	base := time.Date(2026, 9, 7, 22, 0, 0, 0, time.UTC)
	var starts []time.Time
	for i := 0; i < 60; i++ {
		starts = append(starts, base.Add(time.Duration(i)*time.Second))
	}
	r := readerOver(streamOf(starts))

	got, err := r.Spans(Window{From: base.Add(10 * time.Second), To: base.Add(20 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 10 {
		t.Errorf("want 10 spans in a 10s window of 1/s, got %d", len(got))
	}
	for _, sp := range got {
		if sp.Start.Before(base.Add(10*time.Second)) || !sp.Start.Before(base.Add(20*time.Second)) {
			t.Errorf("span at %s escaped the window", sp.Start)
		}
	}
}

// The end is exclusive so two adjacent windows partition the stream instead of
// both claiming the boundary span. A baseline and an incident that overlap by
// one span is a small error; one that overlaps by a busy second is not.
func TestAdjacentWindowsDoNotOverlap(t *testing.T) {
	base := time.Date(2026, 9, 7, 22, 0, 0, 0, time.UTC)
	var starts []time.Time
	for i := 0; i < 30; i++ {
		starts = append(starts, base.Add(time.Duration(i)*time.Second))
	}
	r := readerOver(streamOf(starts))

	first, _ := r.Spans(Window{From: base, To: base.Add(10 * time.Second)})
	second, _ := r.Spans(Window{From: base.Add(10 * time.Second), To: base.Add(20 * time.Second)})

	seen := map[string]bool{}
	for _, sp := range first {
		seen[sp.SpanID] = true
	}
	for _, sp := range second {
		if seen[sp.SpanID] {
			t.Errorf("span %s claimed by both windows", sp.SpanID)
		}
	}
	if len(first)+len(second) != 20 {
		t.Errorf("two 10s windows should hold 20 spans, got %d + %d", len(first), len(second))
	}
}

// Arrival order is not start-time order — the collector batches, and a span
// that started earlier can be written later. A reader that stopped at the first
// span past the window would silently drop the rest.
func TestOutOfOrderArrivalsAreStillFound(t *testing.T) {
	base := time.Date(2026, 9, 7, 22, 0, 0, 0, time.UTC)
	starts := []time.Time{
		base.Add(50 * time.Second), // late arrival, written first
		base.Add(2 * time.Second),
		base.Add(80 * time.Second),
		base.Add(5 * time.Second),
	}
	r := readerOver(streamOf(starts))

	got, err := r.Spans(Window{From: base, To: base.Add(10 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("want both in-window spans despite arrival order, got %d", len(got))
	}
}

func TestBoundsDescribeWhatTheStreamCovers(t *testing.T) {
	base := time.Date(2026, 9, 7, 22, 0, 0, 0, time.UTC)
	starts := []time.Time{base.Add(30 * time.Second), base, base.Add(90 * time.Second)}
	r := readerOver(streamOf(starts))

	w, n, err := r.Bounds()
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("want 3 spans, got %d", n)
	}
	if !w.From.Equal(base) {
		t.Errorf("From = %s, want %s", w.From, base)
	}
	// To is exclusive, so the last span must fall inside the reported bounds.
	if !w.Contains(base.Add(90 * time.Second)) {
		t.Error("bounds must contain the last span")
	}
}

func TestAnEmptyWindowIsNotAnError(t *testing.T) {
	base := time.Date(2026, 9, 7, 22, 0, 0, 0, time.UTC)
	r := readerOver(streamOf([]time.Time{base, base.Add(time.Second)}))

	got, err := r.Spans(Window{From: base.Add(time.Hour), To: base.Add(2 * time.Hour)})
	if err != nil {
		t.Fatalf("a window with no spans is a finding, not a failure: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want no spans, got %d", len(got))
	}
}
