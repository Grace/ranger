// Package source loads spans into ranger.
//
// It reads OTLP/JSON — the format the OpenTelemetry Collector's file exporter
// writes — rather than querying an observability vendor's API.
//
// That is a deliberate constraint, not a shortcut. Honeycomb's Query Data API
// is Enterprise-only, and so is their MCP server; a project whose entire claim
// is a reproducible accuracy number cannot put its data source behind a tier
// most readers do not have. Reading the collector's own output means anyone
// with the OpenTelemetry Demo and a config file can rerun the number, and it
// means ranger is not coupled to any one backend. Point the same collector at
// Honeycomb as a second exporter and both see identical traces.
package source

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/Grace/ranger/internal/trace"
)

// otlpFile is one line of the collector's file exporter output.
type otlpFile struct {
	ResourceSpans []struct {
		Resource struct {
			Attributes []otlpAttr `json:"attributes"`
		} `json:"resource"`
		ScopeSpans []struct {
			Spans []otlpSpan `json:"spans"`
		} `json:"scopeSpans"`
	} `json:"resourceSpans"`
}

type otlpAttr struct {
	Key   string `json:"key"`
	Value struct {
		StringValue *string  `json:"stringValue"`
		IntValue    *string  `json:"intValue"`
		BoolValue   *bool    `json:"boolValue"`
		DoubleValue *float64 `json:"doubleValue"`
	} `json:"value"`
}

func (a otlpAttr) str() string {
	switch {
	case a.Value.StringValue != nil:
		return *a.Value.StringValue
	case a.Value.IntValue != nil:
		return *a.Value.IntValue
	case a.Value.BoolValue != nil:
		return strconv.FormatBool(*a.Value.BoolValue)
	case a.Value.DoubleValue != nil:
		return strconv.FormatFloat(*a.Value.DoubleValue, 'g', -1, 64)
	}
	return ""
}

type otlpSpan struct {
	TraceID           string     `json:"traceId"`
	SpanID            string     `json:"spanId"`
	ParentSpanID      string     `json:"parentSpanId"`
	Name              string     `json:"name"`
	StartTimeUnixNano string     `json:"startTimeUnixNano"`
	EndTimeUnixNano   string     `json:"endTimeUnixNano"`
	Attributes        []otlpAttr `json:"attributes"`
	Status            struct {
		// The collector writes the enum as a string; some SDKs and older
		// collector builds write the integer. Accept either rather than
		// silently reading every span as a success.
		Code json.RawMessage `json:"code"`
	} `json:"status"`
}

// ReadOTLPJSON reads newline-delimited OTLP/JSON and returns the spans.
//
// A malformed line is an error rather than a skip. Quietly dropping spans
// would change the accuracy number without changing anything a reader could
// see, which is the one failure this project cannot afford.
func ReadOTLPJSON(r io.Reader) ([]trace.Span, error) {
	var out []trace.Span

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20) // demo traces produce long lines

	line := 0
	for sc.Scan() {
		line++
		b := sc.Bytes()
		if len(b) == 0 {
			continue
		}

		var f otlpFile
		if err := json.Unmarshal(b, &f); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}

		for _, rs := range f.ResourceSpans {
			res := map[string]string{}
			for _, a := range rs.Resource.Attributes {
				res[a.Key] = a.str()
			}
			service := res["service.name"]

			for _, ss := range rs.ScopeSpans {
				for _, s := range ss.Spans {
					start, err := unixNano(s.StartTimeUnixNano)
					if err != nil {
						return nil, fmt.Errorf("line %d: span %s: start: %w", line, s.SpanID, err)
					}
					end, err := unixNano(s.EndTimeUnixNano)
					if err != nil {
						return nil, fmt.Errorf("line %d: span %s: end: %w", line, s.SpanID, err)
					}

					attrs := make(map[string]string, len(res)+len(s.Attributes))
					for k, v := range res {
						attrs[k] = v
					}
					for _, a := range s.Attributes {
						attrs[a.Key] = a.str()
					}

					out = append(out, trace.Span{
						TraceID:  s.TraceID,
						SpanID:   s.SpanID,
						ParentID: s.ParentSpanID,
						Service:  service,
						Name:     s.Name,
						Start:    start,
						End:      end,
						Status:   statusCode(s.Status.Code),
						Attrs:    attrs,
					})
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// statusCode normalizes OTLP's status enum, which appears in the wild as
// "STATUS_CODE_ERROR", as the bare integer 2, and as absent.
func statusCode(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch s {
		case "STATUS_CODE_ERROR":
			return "ERROR"
		case "STATUS_CODE_OK":
			return "OK"
		}
		return ""
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		switch n {
		case 1:
			return "OK"
		case 2:
			return "ERROR"
		}
	}
	return ""
}

// unixNano parses OTLP's timestamps, which are decimal strings in JSON because
// they do not fit a float64 without losing precision. Some encoders emit them
// as numbers anyway.
func unixNano(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(0, n).UTC(), nil
}
