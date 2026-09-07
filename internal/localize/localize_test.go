package localize

import (
	"testing"
	"time"

	"github.com/Grace/ranger/internal/trace"
)

var base = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

// chain builds n traces of frontend -> cart -> postgres, where postgres does
// pgSelf of the work and the services above it only wait.
func chain(n int, pgSelf, cartSelf time.Duration, cartFails bool) []*trace.Trace {
	var spans []trace.Span
	for i := 0; i < n; i++ {
		t := base.Add(time.Duration(i) * time.Second)
		total := pgSelf + cartSelf + 5*time.Millisecond

		status := ""
		if cartFails {
			status = "ERROR"
		}
		spans = append(spans,
			trace.Span{TraceID: id(i), SpanID: id(i) + "-a", Service: "frontend", Name: "GET /cart",
				Start: t, End: t.Add(total)},
			trace.Span{TraceID: id(i), SpanID: id(i) + "-b", ParentID: id(i) + "-a",
				Service: "cart", Name: "getCart", Status: status,
				Start: t.Add(5 * time.Millisecond), End: t.Add(total)},
			trace.Span{TraceID: id(i), SpanID: id(i) + "-c", ParentID: id(i) + "-b",
				Service: "postgres", Name: "SELECT items",
				Start: t.Add(5*time.Millisecond + cartSelf), End: t.Add(total)},
		)
	}
	return trace.Assemble(spans)
}

func id(i int) string { return string(rune('a'+i/26)) + string(rune('a'+i%26)) }

func localize(t *testing.T, baseline, incident []*trace.Trace) Result {
	t.Helper()
	return Localize(ProfileWindow(baseline), ProfileWindow(incident), DefaultOptions())
}

// The point of the whole project. postgres slows down; frontend and cart both
// get slower too, because they are waiting on it. Only postgres should be
// named, and the other two should be labeled as waiting rather than ranked.
func TestNamesTheServiceThatGotSlowerNotTheOnesWaitingOnIt(t *testing.T) {
	baseline := chain(50, 10*time.Millisecond, 5*time.Millisecond, false)
	incident := chain(50, 200*time.Millisecond, 5*time.Millisecond, false)

	res := localize(t, baseline, incident)
	if !res.Localized {
		t.Fatalf("expected a localization, got none (top score %v)", topScore(res))
	}
	if got := res.Candidates[0].Op.Service; got != "postgres" {
		t.Errorf("blamed %q, want postgres", got)
	}
	if got := res.Candidates[0].Verdict; got != Slower {
		t.Errorf("verdict = %q, want %q", got, Slower)
	}

	for _, c := range res.Candidates[1:] {
		if c.Op.Service == "postgres" {
			continue
		}
		if c.Verdict != WaitingOnSomethingBelow {
			t.Errorf("%s verdict = %q, want %q — its duration moved but its own work did not",
				c.Op, c.Verdict, WaitingOnSomethingBelow)
		}
	}
}

// A dimensional diff sees all three services get slower and has no way to
// order them. This is the claim ranger has to be able to demonstrate, so it
// gets its own test.
func TestEveryServiceOnThePathGetsSlowerButOnlyOneIsRanked(t *testing.T) {
	baseline := chain(50, 10*time.Millisecond, 5*time.Millisecond, false)
	incident := chain(50, 200*time.Millisecond, 5*time.Millisecond, false)

	inc := ProfileWindow(incident)
	bas := ProfileWindow(baseline)
	for _, svc := range []string{"frontend", "cart", "postgres"} {
		op := findOp(t, inc, svc)
		if inc[op].MedianDuration <= bas[op].MedianDuration {
			t.Fatalf("%s duration did not move; the test fixture is wrong", svc)
		}
	}

	res := Localize(bas, inc, DefaultOptions())
	ranked := 0
	for _, c := range res.Candidates {
		if c.Score > 0 {
			ranked++
		}
	}
	if ranked != 1 {
		t.Errorf("%d operations scored above zero, want 1", ranked)
	}
}

func TestBlamesTheServiceThatStartedFailing(t *testing.T) {
	baseline := chain(50, 10*time.Millisecond, 5*time.Millisecond, false)
	incident := chain(50, 10*time.Millisecond, 5*time.Millisecond, true)

	res := localize(t, baseline, incident)
	if !res.Localized {
		t.Fatal("expected a localization")
	}
	top := res.Candidates[0]
	if top.Op.Service != "cart" || top.Verdict != Failing {
		t.Errorf("top = %s / %s, want cart / %s", top.Op, top.Verdict, Failing)
	}
}

// "No code change explains this" has to be a real answer, not a promise the
// README makes.
func TestDeclinesToNameACauseWhenNothingMoved(t *testing.T) {
	baseline := chain(50, 10*time.Millisecond, 5*time.Millisecond, false)
	incident := chain(50, 10*time.Millisecond, 5*time.Millisecond, false)

	res := localize(t, baseline, incident)
	if res.Localized {
		t.Errorf("localized %s on identical windows", res.Candidates[0].Op)
	}
}

// A shift measured from four requests is not a shift.
func TestSkipsOperationsWithTooFewSamples(t *testing.T) {
	baseline := chain(4, 10*time.Millisecond, 5*time.Millisecond, false)
	incident := chain(4, 500*time.Millisecond, 5*time.Millisecond, false)

	res := localize(t, baseline, incident)
	if res.Localized {
		t.Errorf("localized on 4 samples per window")
	}
	if res.Skipped == 0 {
		t.Error("nothing was reported as skipped")
	}
}

func TestRankingIsStableAcrossRuns(t *testing.T) {
	baseline := chain(50, 10*time.Millisecond, 5*time.Millisecond, false)
	incident := chain(50, 200*time.Millisecond, 5*time.Millisecond, false)

	first := localize(t, baseline, incident)
	for i := 0; i < 20; i++ {
		got := localize(t, baseline, incident)
		if len(got.Candidates) != len(first.Candidates) {
			t.Fatalf("run %d: candidate count changed", i)
		}
		for j := range got.Candidates {
			if got.Candidates[j].Op != first.Candidates[j].Op {
				t.Fatalf("run %d: position %d = %s, first run had %s",
					i, j, got.Candidates[j].Op, first.Candidates[j].Op)
			}
		}
	}
}

func topScore(r Result) float64 {
	if len(r.Candidates) == 0 {
		return 0
	}
	return r.Candidates[0].Score
}

func findOp(t *testing.T, m map[trace.Operation]*Profile, service string) trace.Operation {
	t.Helper()
	for op := range m {
		if op.Service == service {
			return op
		}
	}
	t.Fatalf("no operation for service %q", service)
	return trace.Operation{}
}

// Real traces, not the synthetic ones above, found this: a caller of a service
// that slowed by 1.9s picks up a few milliseconds of its own noise in the same
// window. That clears any absolute floor, and testing the floor before the
// proportion labelled the caller "slower" when its duration had moved five
// hundred times more than its own work.
//
// The numbers here are the ones the OpenTelemetry Demo actually produced under
// adManualGc: self +3.6ms against duration +1885.6ms.
func TestACallerWithItsOwnNoiseIsStillAWaiter(t *testing.T) {
	baselineTraces := chain(60, 7*time.Millisecond, 5*time.Millisecond, false)
	// The callee gains 1.89s; the caller gains that plus 3.6ms of its own.
	incidentTraces := chain(60, 1893*time.Millisecond, 5*time.Millisecond+3600*time.Microsecond, false)

	res := localize(t, baselineTraces, incidentTraces)
	if !res.Localized {
		t.Fatal("expected a localization")
	}
	if got := res.Candidates[0].Op.Service; got != "postgres" {
		t.Errorf("blamed %q, want postgres", got)
	}

	for _, c := range res.Candidates {
		if c.Op.Service != "frontend" {
			continue
		}
		if c.Verdict != WaitingOnSomethingBelow {
			t.Errorf("frontend verdict = %q (self %v, duration %v), want %q",
				c.Verdict, c.SelfTimeShift, c.DurationShift, WaitingOnSomethingBelow)
		}
		if c.Score != 0 {
			t.Errorf("frontend scored %v, want 0 — a waiter is never a cause", c.Score)
		}
	}
}

// Excluding a service is a judgment call and therefore a place to hide a bad
// number. Result has to carry the list back out so a published figure cannot
// omit it by accident.
func TestExcludedServicesAreDroppedAndReportedBack(t *testing.T) {
	baseline := chain(50, 10*time.Millisecond, 5*time.Millisecond, false)
	incident := chain(50, 200*time.Millisecond, 5*time.Millisecond, false)

	opt := DefaultOptions()
	opt.ExcludeServices = []string{"postgres"}
	res := Localize(ProfileWindow(baseline), ProfileWindow(incident), opt)

	for _, c := range res.Candidates {
		if c.Op.Service == "postgres" {
			t.Fatalf("postgres was excluded but still ranked at %v", c.Score)
		}
	}
	if res.ExcludedOps == 0 {
		t.Error("ExcludedOps = 0; the exclusion dropped nothing")
	}
	if len(res.Excluded) != 1 || res.Excluded[0] != "postgres" {
		t.Errorf("Excluded = %v, want [postgres] echoed back", res.Excluded)
	}
	// With the real cause removed, the remaining operations are all waiters,
	// so ranger should decline rather than promote one of them.
	if res.Localized {
		t.Errorf("localized %s after the cause was excluded", res.Candidates[0].Op)
	}
}

func TestNothingIsExcludedByDefault(t *testing.T) {
	res := Localize(
		ProfileWindow(chain(50, 10*time.Millisecond, 5*time.Millisecond, false)),
		ProfileWindow(chain(50, 200*time.Millisecond, 5*time.Millisecond, false)),
		DefaultOptions(),
	)
	if res.ExcludedOps != 0 || len(res.Excluded) != 0 {
		t.Errorf("default options excluded %d ops (%v); nothing should be dropped unless asked",
			res.ExcludedOps, res.Excluded)
	}
}
