package narrate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Grace/ranger/internal/localize"
	"github.com/Grace/ranger/internal/trace"
)

func cand(service, name string, v localize.Verdict, self time.Duration, rel float64) localize.Candidate {
	return localize.Candidate{
		Op:            trace.Operation{Service: service, Name: name},
		Verdict:       v,
		SelfTimeShift: self,
		RelativeShift: rel,
	}
}

// localized is the adManualGc shape: the ad service's own work moved, and the
// frontend's span for the same call moved by almost as much in duration while
// its own work did not. Telling those apart is the entire point of the tool,
// and it is the mistake a narrator is most likely to repeat.
func localized() localize.Result {
	return localize.Result{
		Localized:  true,
		Considered: 88,
		Skipped:    12,
		Candidates: []localize.Candidate{
			cand("ad", "oteldemo.AdService/GetAds", localize.Slower, 2096*time.Millisecond, 484.0),
			cand("frontend", "oteldemo.AdService/GetAds", localize.WaitingOnSomethingBelow, 8*time.Millisecond, 0.02),
		},
	}
}

func declined() localize.Result {
	return localize.Result{
		Localized:  false,
		Considered: 88,
		Skipped:    12,
		Candidates: []localize.Candidate{
			cand("ad", "oteldemo.AdService/GetAds", localize.Slower, 3*time.Millisecond, 0.7),
		},
	}
}

func TestDisabledByDefault(t *testing.T) {
	n := New(Options{})
	if n.Enabled() {
		t.Fatal("narration must be off unless an endpoint is configured")
	}
	if _, err := n.Narrate(context.Background(), localized()); !errors.Is(err, ErrDisabled) {
		t.Fatalf("want ErrDisabled, got %v", err)
	}
}

// The guardrail this package exists for: prose that renames the culprit is
// discarded, not shown. Naming the caller instead of the callee is the
// specific failure, because both spans did get slower and only the DAG walk
// separates them.
func TestCheckRejectsARenamedCulprit(t *testing.T) {
	err := Check("The frontend service is the most likely cause of the slowdown.", localized())
	if !errors.Is(err, ErrContradicted) {
		t.Fatalf("prose naming the victim must be discarded, got %v", err)
	}
}

func TestCheckRejectsProseLeadingWithTheVictim(t *testing.T) {
	err := Check("frontend slowed sharply, downstream of ad which also moved.", localized())
	if !errors.Is(err, ErrContradicted) {
		t.Fatalf("prose leading with the victim must be discarded, got %v", err)
	}
}

func TestCheckAcceptsProseNamingTheCulpritFirst(t *testing.T) {
	if err := Check("ad is the most likely cause; frontend is waiting on it.", localized()); err != nil {
		t.Fatalf("correct prose was rejected: %v", err)
	}
}

// A declined localization is an answer. A narrator that supplies a cause anyway
// has converted "we do not know" into a name someone will go wake up.
func TestCheckRejectsACauseWhenTheEngineDeclined(t *testing.T) {
	err := Check("This was probably the ad service under memory pressure.", declined())
	if !errors.Is(err, ErrContradicted) {
		t.Fatalf("invented cause after a decline must be discarded, got %v", err)
	}
}

func TestCheckAcceptsAnHonestDecline(t *testing.T) {
	if err := Check("No operation in this window explains the symptom.", declined()); err != nil {
		t.Fatalf("honest decline was rejected: %v", err)
	}
}

// Facts is the model's entire input. If a span, a trace id or request content
// can reach it, the privacy claim in the package doc is false.
func TestFactsCarryConclusionsNotSpans(t *testing.T) {
	f, err := Facts(localized())
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"trace_id", "traceid", "span_id", "spanid"} {
		if strings.Contains(strings.ToLower(f), banned) {
			t.Errorf("facts leaked %q to the model:\n%s", banned, f)
		}
	}
	for _, want := range []string{"ad · oteldemo.AdService/GetAds", "operations ranked: 88", "waiting on something below it"} {
		if !strings.Contains(f, want) {
			t.Errorf("facts missing %q:\n%s", want, f)
		}
	}
}

func TestFactsSayWhenNothingWasLocalized(t *testing.T) {
	f, err := Facts(declined())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f, "no operation in this window explains the symptom") {
		t.Errorf("a decline must be stated to the model, not implied:\n%s", f)
	}
}

func chatServer(t *testing.T, reply string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("request was not JSON: %v", err)
		}
		if body["temperature"] != float64(0) {
			t.Errorf("narration must be deterministic; temperature was %v", body["temperature"])
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": reply}}},
		})
	}))
}

func TestNarrateReturnsProseThatAgreesWithTheRanking(t *testing.T) {
	s := chatServer(t, "ad is the most likely cause: its own work grew by about two seconds. frontend is waiting on it.")
	defer s.Close()

	n := New(Options{Endpoint: s.URL, Model: "test"})
	got, err := n.Narrate(context.Background(), localized())
	if err != nil {
		t.Fatalf("narration failed: %v", err)
	}
	if !strings.Contains(got, "ad") {
		t.Errorf("narration did not name the culprit: %q", got)
	}
}

// End to end: a model that names the wrong service must not reach the caller,
// however fluent it was.
func TestNarrateDiscardsAContradictingModel(t *testing.T) {
	s := chatServer(t, "The frontend service is responsible for the latency increase.")
	defer s.Close()

	n := New(Options{Endpoint: s.URL, Model: "test"})
	if _, err := n.Narrate(context.Background(), localized()); !errors.Is(err, ErrContradicted) {
		t.Fatalf("want ErrContradicted, got %v", err)
	}
}

func TestNarrateFailsClosedOnATransportError(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer s.Close()

	n := New(Options{Endpoint: s.URL, Model: "test"})
	if _, err := n.Narrate(context.Background(), localized()); err == nil {
		t.Fatal("a failing endpoint must return an error, not empty prose")
	}
}

// The fallback has to stand on its own, because it is what ships whenever the
// model is off, slow, or wrong — which is the common case.
func TestSummaryNeedsNoModel(t *testing.T) {
	got := Summary(localized())
	if !strings.Contains(got, "ad · oteldemo.AdService/GetAds") || !strings.Contains(got, "slower") {
		t.Errorf("summary does not carry the answer: %q", got)
	}
	if d := Summary(declined()); !strings.Contains(d, "No operation") {
		t.Errorf("declined summary must say so: %q", d)
	}
}
