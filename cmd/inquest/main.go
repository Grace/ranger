// Command inquest localizes a regression to an operation and writes the
// evidence out as a self-contained HTML page.
//
//	inquest localize -baseline base.jsonl -incident incident.jsonl -out report.html
//	inquest demo -out report.html
//
// Both inputs are newline-delimited OTLP/JSON, the format the OpenTelemetry
// Collector's file exporter writes.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Grace/inquest/internal/localize"
	"github.com/Grace/inquest/internal/report"
	"github.com/Grace/inquest/internal/source"
	"github.com/Grace/inquest/internal/trace"
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
		fmt.Fprintln(os.Stderr, "inquest:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `inquest — deterministic root-cause localization

  inquest localize -baseline <file> -incident <file> [-out report.html]
  inquest demo [-out report.html]
  inquest eval -cases <manifest.json>

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

	window := fmt.Sprintf("%s → %s", filepath.Base(*basePath), filepath.Base(*incPath))
	return emit(baseline, incident, opt, window, *out, *maxTraces)
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

func emit(baseline, incident []*trace.Trace, opt localize.Options, window, out string, maxTraces int) error {
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

	if err := report.Render(f, report.BuildExplorer(res, baseline, incident, window, maxTraces)); err != nil {
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
	fmt.Fprintf(os.Stderr, "wrote %s\n", out)
	return nil
}
