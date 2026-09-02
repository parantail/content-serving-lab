package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/parantail/content-serving-lab/internal/e1phaseb"
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
	case "_serve":
		return runServer(args[1:])
	default:
		return usageError()
	}
}

func runExperiment(args []string) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	defaultRunID := "e1-phase-b-" + time.Now().UTC().Format("20060102T150405Z")
	runID := flags.String("run-id", defaultRunID, "result directory name")
	resultsRoot := flags.String("results-root", "/results-phase-b", "directory containing Phase B result directories")
	fixture := flags.String("fixture", "/app/fixtures/landscape-4928x3264.jpg", "fixture path")
	image := flags.String("container-image", "content-serving-e1:local", "container image identifier recorded in run.json")
	calibration := flags.Bool("calibration", false, "mark the run as excluded calibration")
	repetitions := flags.Int("repetitions", 10, "independent repetitions per scenario")
	transformConcurrency := flags.Int("transform-concurrency", media.DefaultTransformConcurrency, "maximum concurrent image transformations per process")
	requestTimeout := flags.Duration("request-timeout", 90*time.Second, "per-request timeout")
	transformTimeout := flags.Duration("transform-timeout", 60*time.Second, "shared transform timeout")
	startSkewLimit := flags.Duration("start-skew-limit", 100*time.Millisecond, "maximum valid barrier request start skew; zero disables validation")
	resourceGap := flags.Duration("resource-sample-gap", 10*time.Millisecond, "resource sample interval")
	if err := flags.Parse(args); err != nil {
		return err
	}
	output, err := e1phaseb.Execute(e1phaseb.Config{
		RunID: *runID, ResultsRoot: *resultsRoot, FixturePath: *fixture, GitCommit: gitCommit, ContainerImage: *image,
		Command: append([]string{"e1-phase-b", "run"}, args...), Calibration: *calibration, Repetitions: *repetitions,
		TransformConcurrency: *transformConcurrency, RequestTimeout: *requestTimeout, TransformTimeout: *transformTimeout,
		StartSkewLimit: *startSkewLimit, ResourceSampleGap: *resourceGap,
	})
	if err != nil {
		return err
	}
	analysis, err := e1phaseb.Analyze(output.Directory)
	if err != nil {
		return fmt.Errorf("analyze result: %w", err)
	}
	fmt.Printf("result_directory=%s trials=%d calibration=%t\n", output.Directory, len(output.Trials), output.Metadata.Calibration)
	fmt.Printf("analysis_directory=%s valid=%d invalid=%d raw_validation=passed\n", analysis.Directory, analysis.ValidTrials, analysis.InvalidTrials)
	return nil
}

func analyzeExperiment(args []string) error {
	flags := flag.NewFlagSet("analyze", flag.ContinueOnError)
	runDirectory := flags.String("run-dir", "", "Phase B run result directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *runDirectory == "" {
		return fmt.Errorf("analyze requires --run-dir")
	}
	output, err := e1phaseb.Analyze(*runDirectory)
	if err != nil {
		return err
	}
	fmt.Printf("analysis_directory=%s run_id=%s valid=%d invalid=%d raw_validation=passed\n", output.Directory, output.RunID, output.ValidTrials, output.InvalidTrials)
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
	return e1phaseb.ExecuteTrialFile(*input, *output)
}

func runServer(args []string) error {
	flags := flag.NewFlagSet("_serve", flag.ContinueOnError)
	readyFile := flags.String("ready-file", "", "path written when the server is listening")
	fixture := flags.String("fixture", "", "source fixture path")
	derivativeDirectory := flags.String("derivative-dir", "", "shared derivative directory")
	taskID := flags.String("task-id", "", "local process identifier")
	transformTimeout := flags.Duration("transform-timeout", 60*time.Second, "shared transform timeout")
	transformConcurrency := flags.Int("transform-concurrency", media.DefaultTransformConcurrency, "maximum concurrent transforms")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *readyFile == "" || *fixture == "" || *derivativeDirectory == "" || *taskID == "" {
		return fmt.Errorf("_serve requires ready-file, fixture, derivative-dir and task-id")
	}
	return e1phaseb.Serve(e1phaseb.ServerConfig{
		ReadyFile: *readyFile, FixturePath: *fixture, DerivativeDirectory: *derivativeDirectory, TaskID: *taskID,
		TransformTimeout: *transformTimeout, TransformConcurrency: *transformConcurrency,
	})
}

func usageError() error {
	return fmt.Errorf("usage: e1-phase-b <run|analyze> [flags]")
}
