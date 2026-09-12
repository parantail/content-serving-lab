// Command readme-figures regenerates the summary charts in docs/figures from
// the retained analysis files of E1, E2 and E3.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/parantail/content-serving-lab/internal/readmefigures"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("readme-figures", flag.ContinueOnError)
	repoRoot := flags.String("repo-root", ".", "repository root containing experiments/ and reports/")
	outputDir := flags.String("output-dir", "docs/figures", "directory that receives the SVG files, relative to the repository root unless absolute")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !filepath.IsAbs(*outputDir) {
		*outputDir = filepath.Join(*repoRoot, *outputDir)
	}
	figures, err := readmefigures.Generate(readmefigures.DefaultSources(*repoRoot))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*outputDir, 0o755); err != nil {
		return err
	}
	for _, figure := range figures {
		path := filepath.Join(*outputDir, figure.Name)
		if err := os.WriteFile(path, figure.SVG, 0o644); err != nil {
			return err
		}
		fmt.Println(path)
	}
	return nil
}
