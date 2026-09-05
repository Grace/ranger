package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/Grace/inquest/internal/eval"
	"github.com/Grace/inquest/internal/localize"
	"github.com/Grace/inquest/internal/trace"
)

// evalCmd scores inquest against a manifest of labeled incidents.
func evalCmd(args []string) error {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	manifest := fs.String("cases", "", "JSON manifest of labeled cases")
	dir := fs.String("dir", "", "directory the manifest's file paths are relative to (default: the manifest's own directory)")
	minSamples := fs.Int("min-samples", 20, "observations required in both windows to rank an operation")
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
	printSummary(sum)

	// A run in which inquest confidently named the wrong service is a failing
	// run, whatever the hit rate was. Exiting non-zero makes that impossible
	// to skim past in CI.
	if sum.Wrong > 0 {
		return fmt.Errorf("%d of %d cases named the wrong service", sum.Wrong, sum.Total)
	}
	return nil
}

func printSummary(s eval.Summary) {
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
	fmt.Println("\nwrong = named a service that was not responsible. declined = said nothing explains this.")
	fmt.Println("They are reported separately on purpose: a localizer that is quiet when unsure is")
	fmt.Println("usable, and one that is confidently wrong is not, and both look the same in a hit rate.")
}
