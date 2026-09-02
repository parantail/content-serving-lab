package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/parantail/content-serving-lab/internal/e1runner"
	"github.com/parantail/content-serving-lab/internal/media"
)

var gitCommit = "unknown"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usageError()
	}
	switch args[0] {
	case "run":
		return runExperiment(args[1:])
	case "analyze":
		return analyzeExperiment(args[1:])
	case "_trial":
		return runIsolatedTrial(args[1:])
	default:
		return usageError()
	}
}

func runExperiment(args []string) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	defaultRunID := "e1-" + time.Now().UTC().Format("20060102T150405Z")
	runID := flags.String("run-id", defaultRunID, "result directory name")
	resultsRoot := flags.String("results-root", "/results", "directory containing run result directories")
	fixture := flags.String("fixture", "/app/fixtures/landscape-4928x3264.jpg", "fixture path")
	image := flags.String("container-image", "content-serving-e1:local", "container image identifier recorded in run.json")
	calibration := flags.Bool("calibration", false, "mark the run as excluded calibration")
	repetitions := flags.Int("repetitions", 10, "independent repetitions per success scenario")
	transformConcurrency := flags.Int("transform-concurrency", media.DefaultTransformConcurrency, "maximum concurrent image transformations")
	requestTimeout := flags.Duration("request-timeout", 90*time.Second, "per-request timeout")
	transformTimeout := flags.Duration("transform-timeout", 60*time.Second, "shared transform timeout")
	startSkewLimit := flags.Duration("start-skew-limit", 100*time.Millisecond, "maximum valid first-to-last request start skew; zero disables validation")
	resourceGap := flags.Duration("resource-sample-gap", 10*time.Millisecond, "resource sample interval")
	if err := flags.Parse(args); err != nil {
		return err
	}

	output, err := e1runner.Execute(e1runner.Config{
		RunID:                *runID,
		ResultsRoot:          *resultsRoot,
		FixturePath:          *fixture,
		GitCommit:            gitCommit,
		ContainerImage:       *image,
		Command:              append([]string{"e1-runner", "run"}, args...),
		Calibration:          *calibration,
		Repetitions:          *repetitions,
		TransformConcurrency: *transformConcurrency,
		RequestTimeout:       *requestTimeout,
		TransformTimeout:     *transformTimeout,
		StartSkewLimit:       *startSkewLimit,
		ResourceSampleGap:    *resourceGap,
	})
	if err != nil {
		return err
	}
	analysis, err := e1runner.Analyze(output.Directory)
	if err != nil {
		return fmt.Errorf("analyze result: %w", err)
	}

	valid := 0
	for _, trial := range output.Trials {
		if trial.Valid {
			valid++
		}
	}
	fmt.Printf("result_directory=%s trials=%d valid=%d calibration=%t\n", output.Directory, len(output.Trials), valid, output.Metadata.Calibration)
	fmt.Printf("analysis_directory=%s raw_validation=passed\n", analysis.Directory)
	return nil
}

func analyzeExperiment(args []string) error {
	flags := flag.NewFlagSet("analyze", flag.ContinueOnError)
	runDirectory := flags.String("run-dir", "", "run result directory containing run.json and raw CSV files")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *runDirectory == "" {
		return fmt.Errorf("analyze requires --run-dir")
	}
	output, err := e1runner.Analyze(*runDirectory)
	if err != nil {
		return err
	}
	fmt.Printf("analysis_directory=%s run_id=%s raw_validation=passed\n", output.Directory, output.RunID)
	return nil
}

func runIsolatedTrial(args []string) error {
	flags := flag.NewFlagSet("_trial", flag.ContinueOnError)
	input := flags.String("input", "", "trial invocation JSON")
	output := flags.String("output", "", "trial output JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *input == "" || *output == "" {
		return fmt.Errorf("_trial requires --input and --output")
	}
	return e1runner.ExecuteTrialFile(*input, *output)
}

func usageError() error {
	return fmt.Errorf("usage: e1-runner <run|analyze> [flags]")
}
