package main

import (
	"flag"
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/Grace/inquest/internal/localize"
	"github.com/Grace/inquest/internal/trace"
)

// demoCmd generates two windows of synthetic traces with a known cause and
// localizes them.
//
// This exists so the report can be looked at without standing up a collector,
// and so the shape of the output is reviewable on its own. It is not evidence
// of anything: the generator decides the answer, so inquest finding it proves
// only that the plumbing works. The accuracy number has to come from traces
// inquest did not author.
func demoCmd(args []string) error {
	fs := flag.NewFlagSet("demo", flag.ExitOnError)
	out := fs.String("out", "report.html", "where to write the report")
	traces := fs.Int("traces", 400, "traces per window")
	seed := fs.Int64("seed", 1, "RNG seed; the same seed gives the same windows")
	if err := fs.Parse(args); err != nil {
		return err
	}

	baseline := synth(*traces, rand.New(rand.NewSource(*seed)), 12*time.Millisecond)
	incident := synth(*traces, rand.New(rand.NewSource(*seed+1)), 190*time.Millisecond)

	fmt.Fprintln(os.Stderr, "demo: postgres 'SELECT items' slowed 12ms → 190ms; everything above it waits on that")
	return emit(baseline, incident, localize.DefaultOptions(),
		fmt.Sprintf("synthetic demo · %d traces per window", *traces), *out, 300, 0)
}

// synth builds a small storefront: a frontend entry point that fans out to
// cart and shipping, with cart calling postgres. pgSelf is the only thing that
// changes between the two windows.
func synth(n int, rng *rand.Rand, pgSelf time.Duration) []*trace.Trace {
	base := time.Date(2026, 9, 5, 17, 0, 0, 0, time.UTC)
	var spans []trace.Span

	jitter := func(d time.Duration, spread float64) time.Duration {
		f := 1 + spread*(rng.Float64()*2-1)
		if f < 0.05 {
			f = 0.05
		}
		return time.Duration(float64(d) * f)
	}

	for i := 0; i < n; i++ {
		tid := fmt.Sprintf("%016x", rng.Uint64())
		t0 := base.Add(time.Duration(i) * 250 * time.Millisecond)

		feSelf := jitter(4*time.Millisecond, 0.4)
		cartSelf := jitter(6*time.Millisecond, 0.4)
		pg := jitter(pgSelf, 0.35)
		shipSelf := jitter(30*time.Millisecond, 0.3)

		// frontend waits on the slower of its two children.
		cartTotal := cartSelf + pg
		child := cartTotal
		if shipSelf > child {
			child = shipSelf
		}
		feTotal := feSelf + child

		sp := func(id, parent, svc, name string, start, dur time.Duration, status string) trace.Span {
			return trace.Span{
				TraceID: tid, SpanID: tid + "-" + id, Service: svc, Name: name,
				Start: t0.Add(start), End: t0.Add(start + dur), Status: status,
				ParentID: func() string {
					if parent == "" {
						return ""
					}
					return tid + "-" + parent
				}(),
				Attrs: map[string]string{"service.name": svc, "service.version": "1.4.0"},
			}
		}

		spans = append(spans,
			sp("a", "", "frontend", "GET /api/cart", 0, feTotal, ""),
			sp("b", "a", "cartservice", "CartService/GetCart", feSelf, cartTotal, ""),
			sp("c", "b", "postgres", "SELECT items", feSelf+cartSelf, pg, ""),
			sp("d", "a", "shippingservice", "ShippingService/GetQuote", feSelf, shipSelf, ""),
		)

		// A steady trickle of failures in both windows, so an error rate that
		// does not move cannot be mistaken for a signal.
		if rng.Float64() < 0.02 {
			spans[len(spans)-1].Status = "ERROR"
		}
	}
	return trace.Assemble(spans)
}
