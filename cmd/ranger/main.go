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
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Grace/ranger/internal/localize"
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *basePath == "" || *incPath == "" {
		return fmt.Errorf("both -baseline and -incident are required")
	}

	baseline, err := load(*basePath)
	if err != nil {
		return fmt.Errorf("baseline: %w", err)
	}
	incident, err := load(*incPath)
	if err != nil {
		return fmt.Errorf("incident: %w", err)
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

	window := fmt.Sprintf("%s → %s", filepath.Base(*basePath), filepath.Base(*incPath))
	return emit(baseline, incident, opt, window, *out, *maxTraces, *top)
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

func emit(baseline, incident []*trace.Trace, opt localize.Options, window, out string, maxTraces, top int) error {
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
	fmt.Fprintf(os.Stderr, "wrote %s\n", out)
	return nil
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
