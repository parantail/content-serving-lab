package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/parantail/content-serving-lab/internal/e3runner"
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
	default:
		return usageError()
	}
}

func runExperiment(args []string) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	defaultRunID := "e3-" + time.Now().UTC().Format("20060102T150405Z")
	runID := flags.String("run-id", defaultRunID, "result directory name")
	resultsRoot := flags.String("results-root", "/results", "directory containing run result directories")
	fixture := flags.String("fixture", "/app/fixtures/landscape-4928x3264.jpg", "fixture path")
	image := flags.String("container-image", "content-serving-e3:local", "container image identifier recorded in run.json")
	calibration := flags.Bool("calibration", false, "mark the run as excluded calibration")
	storage := flags.String("storage", e3runner.StorageLocal, "local or s3 original/derivative storage")
	originalBucket := flags.String("original-bucket", "", "S3 original bucket (storage=s3)")
	derivativeBucket := flags.String("derivative-bucket", "", "S3 derivative bucket (storage=s3)")
	modes := flags.String("modes", "baseline,bounded-wait,kill-switch", "comma-separated isolation modes")
	faults := flags.String("faults", "none,slow-transform,transform-timeout,transform-error,slow-original", "comma-separated fault kinds")
	repetitions := flags.Int("repetitions", 5, "repetitions per mode and fault combination")
	transformConcurrency := flags.Int("transform-concurrency", media.DefaultTransformConcurrency, "process-wide concurrent transform limit")
	requestTimeout := flags.Duration("request-timeout", 30*time.Second, "client per-request timeout")
	transformTimeout := flags.Duration("transform-timeout", 20*time.Second, "server transform work timeout")
	slotWaitLimit := flags.Duration("slot-wait-limit", 2*time.Second, "bounded-wait slot wait limit")
	faultDelay := flags.Duration("fault-delay", 15*time.Second, "delay applied by slow-transform and slow-original")
	killSwitchOn := flags.Duration("kill-switch-on-delay", 20*time.Second, "kill switch enable offset after fault start")
	killSwitchOff := flags.Duration("kill-switch-off-delay", 10*time.Second, "kill switch disable offset after fault end")
	normal := flags.Duration("normal-duration", 30*time.Second, "normal phase length")
	faultDuration := flags.Duration("fault-duration", 60*time.Second, "fault phase length")
	recovery := flags.Duration("recovery-duration", 60*time.Second, "recovery phase length")
	hitRate := flags.Float64("hit-rate", 20, "hit stream requests per second")
	healthyRate := flags.Float64("healthy-miss-rate", 0.5, "healthy miss stream requests per second")
	poisonedRate := flags.Float64("poisoned-miss-rate", 1, "poisoned miss stream requests per second")
	hitKeys := flags.Int("hit-keys", 4, "number of prewarmed hit keys")
	maxInflight := flags.Int("max-inflight", 200, "per-stream in-flight cap; exceeding it marks generator saturation")
	resourceGap := flags.Duration("resource-sample-gap", 100*time.Millisecond, "cgroup resource sample interval")
	stateGap := flags.Duration("state-sample-gap", 1*time.Second, "server state sample interval")
	bucket := flags.Duration("timeline-bucket", 5*time.Second, "timeline aggregation bucket")
	uploadBucket := flags.String("upload-bucket", "", "optional S3 bucket that receives the result directory after the run")
	uploadPrefix := flags.String("upload-prefix", "experiments/e3-failure-isolation/results", "S3 key prefix for uploaded results")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *uploadBucket != "" {
		defer uploadResults(*uploadBucket, *uploadPrefix, filepath.Join(*resultsRoot, *runID))
	}

	config := e3runner.Config{
		RunID:                *runID,
		ResultsRoot:          *resultsRoot,
		FixturePath:          *fixture,
		GitCommit:            gitCommit,
		ContainerImage:       *image,
		Command:              append([]string{"e3-runner", "run"}, args...),
		Calibration:          *calibration,
		Storage:              *storage,
		OriginalBucket:       *originalBucket,
		DerivativeBucket:     *derivativeBucket,
		Modes:                parseModes(*modes),
		Faults:               parseFaults(*faults),
		Repetitions:          *repetitions,
		TransformConcurrency: *transformConcurrency,
		RequestTimeout:       *requestTimeout,
		TransformTimeout:     *transformTimeout,
		SlotWaitLimit:        *slotWaitLimit,
		FaultDelay:           *faultDelay,
		KillSwitchOnDelay:    *killSwitchOn,
		KillSwitchOffDelay:   *killSwitchOff,
		NormalDuration:       *normal,
		FaultDuration:        *faultDuration,
		RecoveryDuration:     *recovery,
		HitRate:              *hitRate,
		HealthyMissRate:      *healthyRate,
		PoisonedMissRate:     *poisonedRate,
		HitKeys:              *hitKeys,
		MaxInflight:          *maxInflight,
		ResourceSampleGap:    *resourceGap,
		StateSampleGap:       *stateGap,
		TimelineBucket:       *bucket,
	}
	output, err := e3runner.Execute(config)
	if err != nil {
		return err
	}
	analysis, err := e3runner.Analyze(output.Directory)
	if err != nil {
		return fmt.Errorf("analyze result: %w", err)
	}
	fmt.Printf("result_directory=%s trials=%d valid=%d invalid=%d calibration=%t\n", output.Directory, analysis.TotalTrials, analysis.ValidTrials, analysis.InvalidTrials, analysis.Calibration)
	fmt.Printf("analysis_directory=%s raw_validation=passed\n", analysis.Directory)
	return nil
}

// uploadResults copies whatever the run produced, including partial raw
// files after a failure, so a one-shot Task never loses evidence.
func uploadResults(bucket, prefix, directory string) {
	if _, err := os.Stat(directory); err != nil {
		fmt.Fprintf(os.Stderr, "upload skipped: %v\n", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	client, err := e3runner.NewS3Client(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "upload failed: %v\n", err)
		return
	}
	uploaded, err := e3runner.UploadDirectory(ctx, client, bucket, strings.TrimSuffix(prefix, "/")+"/"+filepath.Base(directory), directory)
	if err != nil {
		fmt.Fprintf(os.Stderr, "upload failed after %d objects: %v\n", uploaded, err)
		return
	}
	fmt.Printf("uploaded_objects=%d bucket=%s\n", uploaded, bucket)
}

func analyzeExperiment(args []string) error {
	flags := flag.NewFlagSet("analyze", flag.ContinueOnError)
	runDirectory := flags.String("run-dir", "", "run result directory containing run.json and raw files")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *runDirectory == "" {
		return fmt.Errorf("analyze requires --run-dir")
	}
	output, err := e3runner.Analyze(*runDirectory)
	if err != nil {
		return err
	}
	fmt.Printf("analysis_directory=%s run_id=%s trials=%d valid=%d invalid=%d raw_validation=passed\n", output.Directory, output.RunID, output.TotalTrials, output.ValidTrials, output.InvalidTrials)
	return nil
}

func parseModes(raw string) []media.IsolationMode {
	var modes []media.IsolationMode
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			modes = append(modes, media.IsolationMode(part))
		}
	}
	return modes
}

func parseFaults(raw string) []media.FaultKind {
	var faults []media.FaultKind
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			faults = append(faults, media.FaultKind(part))
		}
	}
	return faults
}

func usageError() error {
	return fmt.Errorf("usage: e3-runner <run|analyze> [flags]")
}
