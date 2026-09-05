package source

import (
	"strings"
	"testing"
	"time"
)

const oneTrace = `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"cart"}},{"key":"service.version","value":{"stringValue":"1.4.0"}}]},"scopeSpans":[{"spans":[{"traceId":"aa","spanId":"b1","parentSpanId":"a1","name":"getCart","startTimeUnixNano":"1788000000000000000","endTimeUnixNano":"1788000000100000000","status":{"code":"STATUS_CODE_ERROR"},"attributes":[{"key":"http.status_code","value":{"intValue":"500"}}]}]}]}]}`

func TestReadsACollectorFileExporterLine(t *testing.T) {
	spans, err := ReadOTLPJSON(strings.NewReader(oneTrace))
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	s := spans[0]

	if s.Service != "cart" || s.Name != "getCart" {
		t.Errorf("service/name = %q/%q", s.Service, s.Name)
	}
	if s.TraceID != "aa" || s.SpanID != "b1" || s.ParentID != "a1" {
		t.Errorf("ids = %q %q %q", s.TraceID, s.SpanID, s.ParentID)
	}
	if got := s.Duration(); got != 100*time.Millisecond {
		t.Errorf("duration = %v, want 100ms", got)
	}
	if !s.Failed() {
		t.Error("STATUS_CODE_ERROR did not read as a failure")
	}
	// Resource attributes have to reach the span: service.version is how a
	// localization gets tied back to a deploy.
	if s.Attrs["service.version"] != "1.4.0" {
		t.Errorf("service.version = %q", s.Attrs["service.version"])
	}
	if s.Attrs["http.status_code"] != "500" {
		t.Errorf("int attribute = %q, want \"500\"", s.Attrs["http.status_code"])
	}
}

// The enum shows up as a string, as an integer, and not at all, depending on
// which SDK and which collector build produced the line. Reading the integer
// form as a success would quietly zero out the error signal.
func TestReadsBothSpellingsOfTheStatusEnum(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		want       bool
	}{
		{"string error", `"STATUS_CODE_ERROR"`, true},
		{"integer error", `2`, true},
		{"string ok", `"STATUS_CODE_OK"`, false},
		{"integer ok", `1`, false},
		{"unset", `0`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := `{"resourceSpans":[{"resource":{"attributes":[]},"scopeSpans":[{"spans":[{"traceId":"a","spanId":"b","name":"x","startTimeUnixNano":"1","endTimeUnixNano":"2","status":{"code":` + tc.code + `}}]}]}]}`
			spans, err := ReadOTLPJSON(strings.NewReader(line))
			if err != nil {
				t.Fatal(err)
			}
			if spans[0].Failed() != tc.want {
				t.Errorf("Failed() = %v, want %v", spans[0].Failed(), tc.want)
			}
		})
	}
}

func TestReadsManyLines(t *testing.T) {
	in := strings.Join([]string{oneTrace, oneTrace, oneTrace}, "\n")
	spans, err := ReadOTLPJSON(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) != 3 {
		t.Errorf("got %d spans, want 3", len(spans))
	}
}

// Silently dropping a malformed line would change the accuracy number without
// changing anything a reader could see.
func TestAMalformedLineIsAnErrorNotASkip(t *testing.T) {
	in := oneTrace + "\n{not json}\n" + oneTrace
	if _, err := ReadOTLPJSON(strings.NewReader(in)); err == nil {
		t.Fatal("expected an error")
	} else if !strings.Contains(err.Error(), "line 2") {
		t.Errorf("error does not say which line: %v", err)
	}
}

func TestRejectsASpanWithNoTimestamps(t *testing.T) {
	line := `{"resourceSpans":[{"resource":{"attributes":[]},"scopeSpans":[{"spans":[{"traceId":"a","spanId":"b","name":"x"}]}]}]}`
	if _, err := ReadOTLPJSON(strings.NewReader(line)); err == nil {
		t.Fatal("expected an error for a span with no start time")
	}
}
