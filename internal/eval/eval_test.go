package eval

import (
	"strings"
	"testing"
	"time"

	"github.com/Grace/inquest/internal/localize"
	"github.com/Grace/inquest/internal/trace"
)

var t0 = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

// chain builds n traces of frontend -> cart -> db where db does dbSelf of the
// work, so the caller can decide which service actually got slower.
func chain(n int, dbSelf, cartSelf time.Duration) []*trace.Trace {
	var spans []trace.Span
	for i := 0; i < n; i++ {
		id := string(rune('a'+i/26)) + string(rune('a'+i%26))
		start := t0.Add(time.Duration(i) * time.Second)
		total := dbSelf + cartSelf + 4*time.Millisecond
		spans = append(spans,
			trace.Span{TraceID: id, SpanID: id + "1", Service: "frontend", Name: "GET /",
				Start: start, End: start.Add(total)},
			trace.Span{TraceID: id, SpanID: id + "2", ParentID: id + "1", Service: "cart", Name: "GetCart",
				Start: start.Add(4 * time.Millisecond), End: start.Add(total)},
			trace.Span{TraceID: id, SpanID: id + "3", ParentID: id + "2", Service: "db", Name: "SELECT",
				Start: start.Add(4*time.Millisecond + cartSelf), End: start.Add(total)},
		)
	}
	return trace.Assemble(spans)
}

func loaderFor(files map[string][]*trace.Trace) Loader {
	return func(path string) ([]*trace.Trace, error) {
		return files[path], nil
	}
}

func TestScoresAHitWhenTheResponsibleServiceRanksFirst(t *testing.T) {
	files := map[string][]*trace.Trace{
		"base": chain(60, 10*time.Millisecond, 5*time.Millisecond),
		"inc":  chain(60, 220*time.Millisecond, 5*time.Millisecond),
	}
	cases := []Case{{Name: "db slow", ExpectService: "db", BaselineFile: "base", IncidentFile: "inc"}}

	s, err := Run(cases, loaderFor(files), localize.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if s.Results[0].Outcome != Correct {
		t.Fatalf("outcome = %q, named %q", s.Results[0].Outcome, s.Results[0].Named)
	}
	if s.Top1() != 1 {
		t.Errorf("top1 = %v, want 1", s.Top1())
	}
}

// The expensive failure mode gets its own bucket. A run that is wrong is not
// the same as a run that stayed quiet, and the summary must not blend them.
func TestNamingTheWrongServiceIsNotTheSameAsDeclining(t *testing.T) {
	files := map[string][]*trace.Trace{
		"base": chain(60, 10*time.Millisecond, 5*time.Millisecond),
		"inc":  chain(60, 220*time.Millisecond, 5*time.Millisecond),
		"flat": chain(60, 10*time.Millisecond, 5*time.Millisecond),
	}
	cases := []Case{
		// Ground truth says cart, but db is what actually moved.
		{Name: "mislabeled", ExpectService: "cart", BaselineFile: "base", IncidentFile: "inc"},
		// Nothing moved at all.
		{Name: "quiet", ExpectService: "db", BaselineFile: "base", IncidentFile: "flat"},
	}

	s, err := Run(cases, loaderFor(files), localize.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if s.Wrong != 1 {
		t.Errorf("wrong = %d, want 1", s.Wrong)
	}
	if s.Declined != 1 {
		t.Errorf("declined = %d, want 1", s.Declined)
	}
	if s.Correct != 0 {
		t.Errorf("correct = %d, want 0", s.Correct)
	}
	if s.WrongRate() != 0.5 || s.DeclineRate() != 0.5 {
		t.Errorf("rates = wrong %v decline %v, want 0.5 / 0.5", s.WrongRate(), s.DeclineRate())
	}
}

// A service that only shows up as "waiting on something below it" has been
// explicitly ruled out as a cause. Counting it as a hit would let inquest mark
// its own homework on the one distinction it claims to make.
func TestAWaiterDoesNotCountAsAHit(t *testing.T) {
	files := map[string][]*trace.Trace{
		"base": chain(60, 10*time.Millisecond, 5*time.Millisecond),
		"inc":  chain(60, 220*time.Millisecond, 5*time.Millisecond),
	}
	// frontend and cart both appear in the ranking, labeled as waiting.
	cases := []Case{{Name: "blame the waiter", ExpectService: "frontend", BaselineFile: "base", IncidentFile: "inc"}}

	s, err := Run(cases, loaderFor(files), localize.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Results[0].Outcome; got != Wrong {
		t.Errorf("outcome = %q, want %q — frontend was ruled out, not found", got, Wrong)
	}
	if s.Results[0].Rank != 0 {
		t.Errorf("rank = %d, want 0 — a waiter holds no rank", s.Results[0].Rank)
	}
}

func TestManifestRequiresGroundTruth(t *testing.T) {
	_, err := ReadManifest(strings.NewReader(
		`[{"name":"x","baselineFile":"a","incidentFile":"b"}]`))
	if err == nil || !strings.Contains(err.Error(), "expectService") {
		t.Fatalf("err = %v, want a complaint about missing ground truth", err)
	}
}

func TestManifestRejectsAnEmptyList(t *testing.T) {
	if _, err := ReadManifest(strings.NewReader(`[]`)); err == nil {
		t.Fatal("expected an error for an empty manifest")
	}
}

func TestManifestSortsCasesForStableReports(t *testing.T) {
	cases, err := ReadManifest(strings.NewReader(`[
	  {"name":"zeta","expectService":"a","baselineFile":"b","incidentFile":"i"},
	  {"name":"alpha","expectService":"a","baselineFile":"b","incidentFile":"i"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if cases[0].Name != "alpha" {
		t.Errorf("first case = %q, want alpha", cases[0].Name)
	}
}
