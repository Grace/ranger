package source

import (
	"bytes"
	"io"
	"time"

	"github.com/Grace/ranger/internal/trace"
)

// A Window is a half-open time range [From, To).
type Window struct {
	From, To time.Time
}

// Contains reports whether t falls in the window. The end is exclusive so two
// adjacent windows cannot both claim the same span.
func (w Window) Contains(t time.Time) bool {
	return !t.Before(w.From) && t.Before(w.To)
}

func (w Window) Duration() time.Duration { return w.To.Sub(w.From) }

func (w Window) String() string {
	return w.From.UTC().Format("15:04:05") + "–" + w.To.UTC().Format("15:04:05 2006-01-02")
}

// Source returns the spans that started inside a window.
//
// Ranger's original shape — two files, each holding one purpose-captured
// window — made every question cost a capture: settle the system, record a
// baseline, inject, record again. Thirteen minutes to ask something that takes
// under a second to answer. Worse, it made the baseline a separate act from the
// incident, and a baseline recorded at a different time is a baseline that has
// drifted. Both complaints are the same design.
//
// A window is the right unit because telemetry already exists. Nothing needs
// capturing; the windows are in the stream already and simply cannot be
// addressed. Selecting a baseline from an hour earlier, or from the same hour
// yesterday, then becomes a flag rather than an afternoon.
//
// The interface is one method so that a store-backed implementation — the
// reason to have an interface at all — is a new file rather than a refactor.
type Source interface {
	Spans(w Window) ([]trace.Span, error)
}

// StreamReader reads a window out of a continuous OTLP/JSON stream: the file
// the collector has been appending to all along, rather than a file someone
// carved out of it in advance.
type StreamReader struct {
	// Open returns a fresh reader over the whole stream. It is a function
	// rather than an io.Reader because a Source is queried more than once —
	// baseline and incident are two calls — and a reader cannot be rewound in
	// general.
	Open func() (io.ReadCloser, error)
}

// Spans implements Source.
//
// This scans the stream and keeps what falls inside the window. A whole-file
// scan is the naive implementation and it is deliberate for now: the file
// exporter writes in arrival order and arrival order is not start-time order,
// so stopping early at the first out-of-range span would silently truncate the
// window. Correct and measured beats clever and wrong, and the cost should be
// measured before it is optimized.
func (s StreamReader) Spans(w Window) ([]trace.Span, error) {
	all, err := s.readAll()
	if err != nil {
		return nil, err
	}

	out := make([]trace.Span, 0, len(all)/8)
	for _, sp := range all {
		if w.Contains(sp.Start) {
			out = append(out, sp)
		}
	}
	return out, nil
}

// readAll parses the stream, tolerating an incomplete final line.
//
// A file the collector is still appending to always ends mid-write, so its last
// line is routinely a fragment. That is expected and benign; a malformed line in
// the middle is corruption and still an error, because silently skipping those
// would hide exactly the kind of problem this project exists to surface. It is
// the same rule capture.sh applies, for the same reason.
func (s StreamReader) readAll() ([]trace.Span, error) {
	rc, err := s.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	// Everything up to the final newline is whole. Anything after it was
	// still being written.
	if i := bytes.LastIndexByte(data, '\n'); i >= 0 {
		data = data[:i+1]
	}
	return ReadOTLPJSON(bytes.NewReader(data))
}

// Windows selects several windows in one pass.
//
// Spans returns one window and rereads the stream to do it, which is the wrong
// shape for the question ranger actually asks: a baseline and an incident, from
// the same stream, every time. Two calls meant two full scans of a file that is
// mostly not in either window. This reads once and sorts spans into whichever
// window claims them.
//
// The result is positional — out[i] holds the spans for ws[i] — so a caller
// cannot mix up which window it asked for.
func (s StreamReader) Windows(ws []Window) ([][]trace.Span, error) {
	all, err := s.readAll()
	if err != nil {
		return nil, err
	}
	out := make([][]trace.Span, len(ws))
	for i := range out {
		out[i] = make([]trace.Span, 0, len(all)/8)
	}
	for _, sp := range all {
		for i, w := range ws {
			if w.Contains(sp.Start) {
				out[i] = append(out[i], sp)
			}
		}
	}
	return out, nil
}

// Bounds reports the first and last span start time in a stream, so a caller
// can choose windows without guessing at what the stream covers.
func (s StreamReader) Bounds() (Window, int, error) {
	all, err := s.readAll()
	if err != nil {
		return Window{}, 0, err
	}
	if len(all) == 0 {
		return Window{}, 0, nil
	}

	lo, hi := all[0].Start, all[0].Start
	for _, sp := range all[1:] {
		if sp.Start.Before(lo) {
			lo = sp.Start
		}
		if sp.Start.After(hi) {
			hi = sp.Start
		}
	}
	// Bounds are inclusive of the last span, and Window.To is exclusive.
	return Window{From: lo, To: hi.Add(time.Nanosecond)}, len(all), nil
}
