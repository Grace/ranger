// Command ranger localizes a regression to an operation and writes the
// evidence out as a self-contained HTML page.
//
//	ranger localize -baseline base.jsonl -incident incident.jsonl -out report.html
//	ranger demo -out report.html
//
// Both inputs are newline-delimited OTLP/JSON, the format the OpenTelemetry
// Collector's file exporter writes.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Grace/ranger/internal/localize"
	"github.com/Grace/ranger/internal/narrate"
	"github.com/Grace/ranger/internal/report"
	"github.com/Grace/ranger/internal/source"
	"github.com/Grace/ranger/internal/trace"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "localize":
		err = localizeCmd(os.Args[2:])
	case "demo":
		err = demoCmd(os.Args[2:])
	case "eval":
		err = evalCmd(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ranger:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `ranger — deterministic root-cause localization

  ranger localize -baseline <file> -incident <file> [-out report.html]
  ranger demo [-out report.html]
  ranger eval -cases <manifest.json>

Inputs are newline-delimited OTLP/JSON from the OpenTelemetry Collector's
file exporter.
`)
}

func localizeCmd(args []string) error {
	fs := flag.NewFlagSet("localize", flag.ExitOnError)
	basePath := fs.String("baseline", "", "OTLP/JSON file for the healthy window")
	incPath := fs.String("incident", "", "OTLP/JSON file for the incident window")
	out := fs.String("out", "report.html", "where to write the report")
	minSamples := fs.Int("min-samples", 20, "observations required in both windows to rank an operation")
	maxTraces := fs.Int("max-traces", 300, "traces embedded in the report, slowest first")
	exclude := fs.String("exclude", "", "comma-separated services to drop from the ranking")
	rank := fs.String("rank", "deviation", "how to score: deviation (robust-z, how surprising) or effect (share of the operation's own baseline)")
	threshold := fs.Float64("threshold", -1, "override the reporting threshold; default 3.0 for deviation, 1.0 for effect")
	top := fs.Int("top", 0, "also print the top N candidates and their arithmetic, including near misses when ranger declines")
	stream := fs.String("stream", "", "continuous OTLP/JSON stream to select windows from, instead of two capture files")
	incidentAt := fs.String("incident-at", "", "RFC3339 start of the incident window when using -stream")
	baselineAt := fs.String("baseline-at", "", "RFC3339 start of the baseline window; default is one window before the incident")
	windowFor := fs.Duration("window", 5*time.Minute, "window length when using -stream")
	perm := fs.Int("permutations", 0, "permutation test iterations for calibrated significance; 0 disables")
	permSeed := fs.Int64("permutation-seed", 1, "seed, so a reported p is reproducible")
	narrateURL := fs.String("narrate", os.Getenv("RANGER_NARRATE_ENDPOINT"), "OpenAI-compatible chat completions URL; when set, the finished ranking is also written up in prose")
	narrateModel := fs.String("narrate-model", envOr("RANGER_NARRATE_MODEL", "gpt-4o-mini"), "model id for -narrate")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var baseline, incident []*trace.Trace
	window := ""

	switch {
	case *stream != "":
		if *basePath != "" || *incPath != "" {
			return fmt.Errorf("-stream selects windows itself; do not also pass -baseline or -incident")
		}
		if *incidentAt == "" {
			return fmt.Errorf("-incident-at is required with -stream")
		}
		incStart, err := time.Parse(time.RFC3339, *incidentAt)
		if err != nil {
			return fmt.Errorf("-incident-at: %w", err)
		}
		// Default the baseline to the window immediately before the incident.
		// Adjacent is the least stale choice available, which is the entire
		// reason for selecting rather than capturing.
		baseStart := incStart.Add(-*windowFor)
		if *baselineAt != "" {
			if baseStart, err = time.Parse(time.RFC3339, *baselineAt); err != nil {
				return fmt.Errorf("-baseline-at: %w", err)
			}
		}

		src := source.StreamReader{Open: func() (io.ReadCloser, error) { return os.Open(*stream) }}

		bw := source.Window{From: baseStart, To: baseStart.Add(*windowFor)}
		iw := source.Window{From: incStart, To: incStart.Add(*windowFor)}

		sets, err := src.Windows([]source.Window{bw, iw})
		if err != nil {
			return fmt.Errorf("reading %s: %w", *stream, err)
		}
		if baseline, err = assemble(sets[0], bw, "baseline"); err != nil {
			return err
		}
		if incident, err = assemble(sets[1], iw, "incident"); err != nil {
			return err
		}
		window = fmt.Sprintf("%s → %s", bw, iw)
		fmt.Fprintf(os.Stderr, "baseline %s · incident %s · gap %s\n",
			bw, iw, incStart.Sub(baseStart.Add(*windowFor)).Round(time.Second))

	case *basePath != "" && *incPath != "":
		var err error
		if baseline, err = load(*basePath); err != nil {
			return fmt.Errorf("baseline: %w", err)
		}
		if incident, err = load(*incPath); err != nil {
			return fmt.Errorf("incident: %w", err)
		}

	default:
		return fmt.Errorf("pass either -baseline and -incident, or -stream with -incident-at")
	}

	opt := localize.DefaultOptions()
	opt.MinSamples = *minSamples
	opt = opt.ApplyRanking(localize.Ranking(*rank), *threshold >= 0, *threshold)
	if opt.Rank != localize.ByDeviation && opt.Rank != localize.ByEffect {
		return fmt.Errorf("-rank must be deviation or effect, got %q", *rank)
	}
	if *exclude != "" {
		for _, s := range strings.Split(*exclude, ",") {
			if s = strings.TrimSpace(s); s != "" {
				opt.ExcludeServices = append(opt.ExcludeServices, s)
			}
		}
	}

	if window == "" {
		window = fmt.Sprintf("%s → %s", filepath.Base(*basePath), filepath.Base(*incPath))
	}
	if *perm > 0 {
		sig := localize.Permute(localize.ProfileWindow(baseline), localize.ProfileWindow(incident), opt, *perm, *permSeed)
		if sig.Assessed {
			fmt.Fprintf(os.Stderr,
				"significance: top score %.2f · p=%.3f over %d permutations · chance reaches %.2f one time in twenty\n",
				sig.Observed, sig.P, sig.Permutations, sig.NullP95)
		}
	}
	return emit(baseline, incident, opt, window, *out, *maxTraces, *top, narrate.Options{
		Endpoint: *narrateURL,
		Model:    *narrateModel,
		Key:      os.Getenv("RANGER_NARRATE_KEY"),
	})
}

func load(path string) ([]*trace.Trace, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	spans, err := source.ReadOTLPJSON(f)
	if err != nil {
		return nil, err
	}
	if len(spans) == 0 {
		return nil, fmt.Errorf("no spans in %s", path)
	}
	return trace.Assemble(spans), nil
}

func emit(baseline, incident []*trace.Trace, opt localize.Options, window, out string, maxTraces, top int, nopt narrate.Options) error {
	res := localize.Localize(
		localize.ProfileWindow(baseline),
		localize.ProfileWindow(incident),
		opt,
	)

	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()

	if err := report.Render(f, report.BuildExplorer(res, baseline, incident, window, maxTraces, opt.Rank)); err != nil {
		return err
	}

	// Say the answer on stderr too. Someone running this in an incident should
	// not have to open a browser to learn whether it found anything.
	if w := res.Window; w.Assessed {
		// Context, not a verdict. How far the typical operation moved says
		// whether the top of the ranking stands above its own background or
		// merely sits at the top of a background that moved — but the spread
		// between quiet baselines is wide enough that no constant turns this
		// into an answer. The permutation p above is the calibrated version.
		fmt.Fprintf(os.Stderr, "window: %d ops · median shift %.2fx · p90 %.2fx · top/median %.0f\n",
			w.Ops, w.MedianShift, w.P90Shift, w.TopToMedian)
	}
	if res.Localized {
		top := res.Candidates[0]
		fmt.Fprintf(os.Stderr, "localized: %s (%s, self time %v → %v)\n",
			top.Op, top.Verdict, top.Baseline.MedianSelfTime, top.Incident.MedianSelfTime)
	} else {
		fmt.Fprintf(os.Stderr, "no operation cleared the reporting threshold (%d ranked, %d skipped)\n",
			res.Considered, res.Skipped)
	}
	if top > 0 {
		printTop(res, top)
	}

	// Prose, if an endpoint was configured. The ranking above was already
	// printed and is unaffected by anything that happens here: a narrator that
	// is slow, broken, or contradicts the ranking costs the reader nothing but
	// a line on stderr.
	if n := narrate.New(nopt); n.Enabled() {
		text, err := n.Narrate(context.Background(), res)
		switch {
		case err == nil:
			fmt.Fprintf(os.Stderr, "\n%s\n", text)
		case errors.Is(err, narrate.ErrContradicted):
			fmt.Fprintf(os.Stderr, "\nnarration discarded: %v\n%s\n", err, narrate.Summary(res))
		default:
			fmt.Fprintf(os.Stderr, "\nnarration unavailable (%v)\n%s\n", err, narrate.Summary(res))
		}
	}

	fmt.Fprintf(os.Stderr, "wrote %s\n", out)
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// printTop shows the ranking itself, not just its winner.
//
// A decline is only trustworthy if you can see what it declined on. Without
// this, "no operation cleared the reporting threshold" is indistinguishable
// from a bug in the parser, and the near misses are the first thing an
// on-call engineer wants when the tool says nothing.
func printTop(res localize.Result, n int) {
	if n > len(res.Candidates) {
		n = len(res.Candidates)
	}
	fmt.Fprintf(os.Stderr, "\n%-46s %-8s %7s %10s %10s %9s %7s %6s\n",
		"OPERATION", "VERDICT", "SCORE", "SELF", "DURATION", "BASE-SELF", "REL", "KIDS")
	for _, c := range res.Candidates[:n] {
		var baseSelf time.Duration
		if c.Baseline != nil {
			baseSelf = c.Baseline.MedianSelfTime
		}
		rel := 0.0
		if baseSelf > 0 {
			rel = float64(c.SelfTimeShift) / float64(baseSelf)
		}
		kids := 0.0
		if c.Incident != nil {
			kids = c.Incident.MedianChildren
		}
		fmt.Fprintf(os.Stderr, "%-46.46s %-8.8s %7.2f %10s %10s %9s %6.0f%% %6.1f\n",
			c.Op.String(), c.Verdict, c.Score,
			shortDur(c.SelfTimeShift), shortDur(c.DurationShift),
			shortDur(baseSelf), 100*rel, kids)
	}
	fmt.Fprintln(os.Stderr)
}

func shortDur(d time.Duration) string {
	if d == 0 {
		return "0"
	}
	sign := "+"
	if d < 0 {
		sign, d = "-", -d
	}
	return sign + d.Round(time.Microsecond).String()
}

// assemble turns one window's spans into traces.
//
// An empty window is an error rather than an empty ranking: no spans means the
// range missed the data, and answering "nothing explains this" to a question
// that was never asked would be worse than failing.
func assemble(spans []trace.Span, w source.Window, label string) ([]*trace.Trace, error) {
	if len(spans) == 0 {
		return nil, fmt.Errorf("%s window %s contains no spans", label, w)
	}
	return trace.Assemble(spans), nil
}
