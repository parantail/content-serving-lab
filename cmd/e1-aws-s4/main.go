package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/parantail/content-serving-lab/internal/e1awss4runner"
	"github.com/parantail/content-serving-lab/internal/media"
)

var gitCommit = "unknown"

const (
	defaultSourceHash = "de206136ed0eeaa42499840f49fa35bd3d57745b8a9580b16f0d6e6393ac7f91"
	defaultRawSpec    = "width=640,height=640,fit=cover,quality=80"
)

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
	runID := flags.String("run-id", "e1-aws-s4-"+time.Now().UTC().Format("20060102T150405Z"), "result directory name")
	resultsRoot := flags.String("results-root", "/results-aws-s4", "directory containing AWS S4 result directories")
	region := flags.String("region", "ap-northeast-2", "AWS region")
	cluster := flags.String("cluster", "", "ECS cluster name or ARN")
	derivativeBucket := flags.String("derivative-bucket", "", "private derivative S3 bucket")
	resultBucket := flags.String("result-bucket", "", "private result S3 bucket")
	resultPrefix := flags.String("result-prefix", "experiments/e1-cache-stampede/results-aws-s4", "result object key prefix")
	containerDigest := flags.String("container-digest", os.Getenv("CONTAINER_IMAGE_DIGEST"), "measured media image digest (sha256:...)")
	sourceHash := flags.String("source-hash", defaultSourceHash, "fixture source SHA-256")
	rawSpec := flags.String("transform-spec", defaultRawSpec, "non-format transform fields")
	repetitions := flags.Int("repetitions", 10, "independent repetitions per scenario")
	calibration := flags.Bool("calibration", false, "mark the run as excluded calibration")
	requestTimeout := flags.Duration("request-timeout", 90*time.Second, "per-request timeout")
	controlTimeout := flags.Duration("control-timeout", 30*time.Second, "time allowed to reach every task control endpoint")
	controlPollGap := flags.Duration("control-poll-gap", 100*time.Millisecond, "delay between control endpoint attempts")
	startSkewLimit := flags.Duration("start-skew-limit", 0, "maximum valid barrier request start skew; required except for calibration")
	expectedWebPBytes := flags.Int64("expected-webp-bytes", 0, "expected response size; zero accepts the verified stored size")

	endpoint1 := flags.String("endpoint-1", "", "internal ALB endpoint for the one-task service")
	endpoint2 := flags.String("endpoint-2", "", "internal ALB endpoint for the two-task service")
	endpoint4 := flags.String("endpoint-4", "", "internal ALB endpoint for the four-task service")
	service1 := flags.String("service-1", "", "one-task ECS service name or ARN")
	service2 := flags.String("service-2", "", "two-task ECS service name or ARN")
	service4 := flags.String("service-4", "", "four-task ECS service name or ARN")
	targetGroup1 := flags.String("target-group-1", "", "one-task target group ARN")
	targetGroup2 := flags.String("target-group-2", "", "two-task target group ARN")
	targetGroup4 := flags.String("target-group-4", "", "four-task target group ARN")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("run does not accept positional arguments")
	}

	spec, err := media.ParseTransformSpec(*rawSpec, media.FormatWebP)
	if err != nil {
		return fmt.Errorf("parse transform spec: %w", err)
	}
	derivativeKey, err := media.DerivativeKey(*sourceHash, spec)
	if err != nil {
		return fmt.Errorf("derive image key: %w", err)
	}

	httpClient := &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   100,
		MaxConnsPerHost:       100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	config := e1awss4runner.Config{
		RunID: *runID, ResultsRoot: *resultsRoot, Region: *region, GitCommit: gitCommit,
		ContainerDigest: *containerDigest, Calibration: *calibration, Repetitions: *repetitions,
		SourceHash: *sourceHash, CanonicalSpec: spec.Canonical(), DerivativeKey: derivativeKey,
		DerivativeBucket: *derivativeBucket, ResultBucket: *resultBucket, ResultPrefix: *resultPrefix,
		RequestTimeout: *requestTimeout, ControlTimeout: *controlTimeout, ControlPollGap: *controlPollGap,
		StartSkewLimit: *startSkewLimit, ExpectedWebPBytes: *expectedWebPBytes,
		Services: []e1awss4runner.ServiceTarget{
			{Scenario: e1awss4runner.ScenarioTask1, Endpoint: *endpoint1, Cluster: *cluster, Service: *service1, TargetGroupARN: *targetGroup1, ListenerPort: 8081, ExpectedTasks: 1},
			{Scenario: e1awss4runner.ScenarioTask2, Endpoint: *endpoint2, Cluster: *cluster, Service: *service2, TargetGroupARN: *targetGroup2, ListenerPort: 8082, ExpectedTasks: 2},
			{Scenario: e1awss4runner.ScenarioTask4, Endpoint: *endpoint4, Cluster: *cluster, Service: *service4, TargetGroupARN: *targetGroup4, ListenerPort: 8084, ExpectedTasks: 4},
		},
	}
	if err := e1awss4runner.ValidateConfig(config); err != nil {
		return err
	}
	dependencies, err := e1awss4runner.NewAWSDependencies(ctx, *region, httpClient)
	if err != nil {
		return err
	}
	output, analysis, err := e1awss4runner.Execute(ctx, config, dependencies)
	if err != nil {
		return err
	}
	fmt.Printf("result_directory=%s trials=%d calibration=%t upload=complete\n", output.Directory, len(output.Trials), output.Metadata.Calibration)
	fmt.Printf("analysis_directory=%s valid=%d invalid=%d raw_validation=passed\n", analysis.Directory, analysis.ValidTrials, analysis.InvalidTrials)
	return nil
}

func analyzeExperiment(args []string) error {
	flags := flag.NewFlagSet("analyze", flag.ContinueOnError)
	runDirectory := flags.String("run-dir", "", "AWS S4 run result directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *runDirectory == "" {
		return fmt.Errorf("analyze requires --run-dir and no positional arguments")
	}
	output, err := e1awss4runner.Analyze(*runDirectory)
	if err != nil {
		return err
	}
	fmt.Printf("analysis_directory=%s run_id=%s valid=%d invalid=%d raw_validation=passed\n", output.Directory, output.RunID, output.ValidTrials, output.InvalidTrials)
	return nil
}

func usageError() error {
	return fmt.Errorf("usage: e1-aws-s4 <run|analyze> [flags]")
}
