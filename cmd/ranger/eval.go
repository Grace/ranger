package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/Grace/ranger/internal/eval"
	"github.com/Grace/ranger/internal/localize"
	"github.com/Grace/ranger/internal/trace"
)

// evalCmd scores ranger against a manifest of labeled incidents.
func evalCmd(args []string) error {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	manifest := fs.String("cases", "", "JSON manifest of labeled cases")
	dir := fs.String("dir", "", "directory the manifest's file paths are relative to (default: the manifest's own directory)")
	minSamples := fs.Int("min-samples", 20, "observations required in both windows to rank an operation")
	rank := fs.String("rank", "deviation", "how to score: deviation (robust-z) or effect (share of the operation's own baseline)")
	threshold := fs.Float64("threshold", -1, "override the reporting threshold; default 3.0 for deviation, 1.0 for effect")
	exclude := fs.String("exclude", "", "comma-separated services to drop from the ranking; reported with the result")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *manifest == "" {
		return fmt.Errorf("-cases is required")
	}

	f, err := os.Open(*manifest)
	if err != nil {
		return err
	}
	cases, err := eval.ReadManifest(f)
	f.Close()
	if err != nil {
		return err
	}

	root := *dir
	if root == "" {
		root = filepath.Dir(*manifest)
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

	loader := func(p string) ([]*trace.Trace, error) {
		if !filepath.IsAbs(p) {
			p = filepath.Join(root, p)
		}
		return load(p)
	}

	sum, err := eval.Run(cases, loader, opt)
	if err != nil {
		return err
	}
	fmt.Printf("ranked by %s, reporting threshold %.2f\n\n", opt.Rank, opt.ReportThreshold)
	printSummary(sum, opt.ExcludeServices)

	// A run in which ranger confidently named the wrong service is a failing
	// run, whatever the hit rate was. Exiting non-zero makes that impossible
	// to skim past in CI.
	if sum.Wrong > 0 {
		return fmt.Errorf("%d of %d cases named the wrong service", sum.Wrong, sum.Total)
	}
	return nil
}

func printSummary(s eval.Summary, excluded []string) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "CASE\tOUTCOME\tRANK\tINQUEST SAID\tEXPECTED")
	for _, r := range s.Results {
		rank := "—"
		if r.Rank > 0 {
			rank = fmt.Sprintf("%d", r.Rank)
		}
		named := r.Named
		if named == "" {
			named = "(declined)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Case.Name, r.Outcome, rank, named, r.Case.ExpectService)
	}
	w.Flush()

	fmt.Printf("\n%d cases · top-1 %.0f%% · top-3 %.0f%% · wrong %.0f%% · declined %.0f%%\n",
		s.Total, 100*s.Top1(), 100*s.Top3Rate(), 100*s.WrongRate(), 100*s.DeclineRate())
	if len(excluded) > 0 {
		fmt.Printf("excluded from every ranking: %s\n", strings.Join(excluded, ", "))
	} else {
		fmt.Println("nothing excluded — every operation in the traces was ranked.")
	}
	fmt.Println("\nwrong = named a service that was not responsible. declined = said nothing explains this.")
	fmt.Println("They are reported separately on purpose: a localizer that is quiet when unsure is")
	fmt.Println("usable, and one that is confidently wrong is not, and both look the same in a hit rate.")
}
