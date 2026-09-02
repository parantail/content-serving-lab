package e1runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parantail/content-serving-lab/internal/httpapi"
	"github.com/parantail/content-serving-lab/internal/media"
)

type scenario struct {
	ID          string
	Mode        string
	Concurrency int
	Failure     bool
}

type RunOutput struct {
	Directory string
	Metadata  RunMetadata
	Trials    []TrialResult
	Requests  []RequestResult
	Resources []ResourceSample
	Metrics   []trialMetrics
}

type trialMetrics struct {
	TrialID  string
	Scenario string
	Snapshot media.MetricsSnapshot
}

func Execute(config Config) (RunOutput, error) {
	if err := validateConfig(config); err != nil {
		return RunOutput{}, err
	}
	fixture, err := os.ReadFile(config.FixturePath)
	if err != nil {
		return RunOutput{}, fmt.Errorf("read fixture: %w", err)
	}
	fixtureDigest := sha256.Sum256(fixture)
	sourceHash := hex.EncodeToString(fixtureDigest[:])
	spec, err := media.ParseTransformSpec("width=640,height=640,fit=cover,quality=80", "webp")
	if err != nil {
		return RunOutput{}, err
	}

	hostname, _ := os.Hostname()
	metadata := NewRunMetadata(config)
	metadata.Hostname = hostname
	metadata.Transformer = media.VipsTransformerVersion()
	metadata.FixturePath = filepath.Base(config.FixturePath)
	metadata.FixtureSHA256 = sourceHash
	metadata.FixtureBytes = len(fixture)
	metadata.SourceHash = sourceHash
	metadata.CanonicalSpec = spec.Canonical()
	metadata.DerivativeWidth = spec.Width
	metadata.DerivativeHeight = spec.Height
	metadata.DerivativeFormat = spec.Format
	metadata.DerivativeQuality = spec.Quality
	metadata.CPUQuota = readCPUQuota()
	metadata.MemoryLimitBytes = readMemoryLimit()
	metadata.Commands = []string{strings.Join(config.Command, " ")}
	metadata.Environment["coordinator_modes"] = media.CoordinatorNone + "," + media.CoordinatorProcessSingle
	metadata.Environment["libvips_concurrency"] = "1"
	metadata.Environment["libvips_operation_cache"] = "disabled"
	metadata.Environment["max_concurrent_transforms"] = strconv.Itoa(config.TransformConcurrency)
	metadata.KnownLimitations = []string{
		"The workload is synthetic loopback HTTP traffic inside one container.",
		"govips can observe context cancellation before and after a native libvips call but cannot interrupt a call already executing in C.",
		"The process RSS includes the server, workload generator and native image library.",
	}

	output := RunOutput{
		Directory: filepath.Join(config.ResultsRoot, config.RunID),
		Metadata:  metadata,
	}
	if err := os.MkdirAll(output.Directory, 0o755); err != nil {
		return RunOutput{}, fmt.Errorf("create result directory: %w", err)
	}
	if err := writeRunOutput(output); err != nil {
		return RunOutput{}, err
	}

	schedule := buildSchedule(config.Repetitions)
	for _, item := range schedule {
		trialID := fmt.Sprintf("%s-r%02d", item.ID, item.repetition)
		output.Metadata.ExecutionOrder = append(output.Metadata.ExecutionOrder, trialID)
		fmt.Fprintf(os.Stderr, "trial_started=%s\n", trialID)
		trial, requests, resources, snapshot, err := executeIsolatedTrial(config, item)
		if err != nil {
			return RunOutput{}, fmt.Errorf("execute %s repetition %d: %w", item.ID, item.repetition, err)
		}
		output.Trials = append(output.Trials, trial)
		output.Requests = append(output.Requests, requests...)
		output.Resources = append(output.Resources, resources...)
		output.Metrics = append(output.Metrics, trialMetrics{TrialID: trial.TrialID, Scenario: trial.Scenario, Snapshot: snapshot})
		if err := writeRunOutput(output); err != nil {
			return RunOutput{}, err
		}
		fmt.Fprintf(os.Stderr, "trial_finished=%s valid=%t transforms=%d p99_ms=%.3f\n", trialID, trial.Valid, trial.TransformAttempts, trial.P99MS)
	}

	if err := writeRunOutput(output); err != nil {
		return RunOutput{}, err
	}
	return output, nil
}

type scheduledScenario struct {
	scenario
	repetition int
}

func buildSchedule(repetitions int) []scheduledScenario {
	schedule := make([]scheduledScenario, 0, repetitions*7+1)
	for repetition := 1; repetition <= repetitions; repetition++ {
		schedule = append(schedule, scheduledScenario{scenario: scenario{ID: "S0", Mode: media.CoordinatorNone, Concurrency: 1}, repetition: repetition})
		for index, concurrency := range []int{10, 50, 100} {
			baseline := scheduledScenario{scenario: scenario{ID: "S1-" + strconv.Itoa(concurrency), Mode: media.CoordinatorNone, Concurrency: concurrency}, repetition: repetition}
			intervention := scheduledScenario{scenario: scenario{ID: "S2-" + strconv.Itoa(concurrency), Mode: media.CoordinatorProcessSingle, Concurrency: concurrency}, repetition: repetition}
			if (repetition+index)%2 == 0 {
				schedule = append(schedule, baseline, intervention)
			} else {
				schedule = append(schedule, intervention, baseline)
			}
		}
	}
	schedule = append(schedule, scheduledScenario{scenario: scenario{ID: "F1", Mode: media.CoordinatorProcessSingle, Concurrency: 10, Failure: true}, repetition: 1})
	return schedule
}

func executeTrial(
	config Config,
	item scheduledScenario,
	fixture []byte,
	sourceHash string,
	spec media.TransformSpec,
	hostname string,
) (TrialResult, []RequestResult, []ResourceSample, media.MetricsSnapshot, error) {
	trialID := fmt.Sprintf("%s-r%02d", item.ID, item.repetition)
	directory, err := os.MkdirTemp("", "content-serving-e1-"+trialID+"-")
	if err != nil {
		return TrialResult{}, nil, nil, media.MetricsSnapshot{}, err
	}
	defer os.RemoveAll(directory)
	originalPath := filepath.Join(directory, "original.jpg")
	if err := os.WriteFile(originalPath, fixture, 0o644); err != nil {
		return TrialResult{}, nil, nil, media.MetricsSnapshot{}, err
	}
	derivatives, err := media.NewLocalDerivativeStore(filepath.Join(directory, "derivatives"))
	if err != nil {
		return TrialResult{}, nil, nil, media.MetricsSnapshot{}, err
	}
	coordinator, err := media.NewCoordinator(item.Mode, config.TransformTimeout)
	if err != nil {
		return TrialResult{}, nil, nil, media.MetricsSnapshot{}, err
	}
	var transformer media.Transformer = media.NewVipsTransformerWithConcurrency(config.TransformConcurrency)
	if item.Failure {
		transformer = &media.DeterministicTransformer{
			Delay:     200 * time.Millisecond,
			Output:    []byte("deterministic-recovery-result"),
			FailFirst: 1,
		}
	}
	metrics := &media.Metrics{}
	processor := media.NewProcessor(
		media.NewFileOriginalStore(map[string]string{sourceHash: originalPath}),
		derivatives,
		transformer,
		coordinator,
		metrics,
	)
	server := httptest.NewServer(httpapi.NewHandler(processor))
	defer server.Close()

	sampler := startResourceSampler(config.RunID, trialID, config.ResourceSampleGap)
	requests := issueBurst(config, item, server.URL, sourceHash, trialID, hostname)
	if item.Failure {
		requests = append(requests, issueRecoveryRequest(config, item, server.URL, sourceHash, trialID, hostname))
	}
	resources := sampler.finish()
	snapshot := metrics.Snapshot()
	trial := summarizeTrial(config, item, trialID, requests, resources, snapshot)
	return trial, requests, resources, snapshot, nil
}

func issueBurst(config Config, item scheduledScenario, serverURL, sourceHash, trialID, hostname string) []RequestResult {
	client := &http.Client{Transport: &http.Transport{
		MaxIdleConns:        item.Concurrency,
		MaxIdleConnsPerHost: item.Concurrency,
		MaxConnsPerHost:     item.Concurrency,
	}}
	defer client.CloseIdleConnections()

	results := make([]RequestResult, item.Concurrency)
	start := make(chan struct{})
	var ready sync.WaitGroup
	var finished sync.WaitGroup
	ready.Add(item.Concurrency)
	finished.Add(item.Concurrency)
	for index := range item.Concurrency {
		go func() {
			defer finished.Done()
			ready.Done()
			<-start
			rawSpec := "width=640,height=640,fit=cover,quality=80"
			if index%2 == 1 {
				rawSpec = "quality=80,fit=cover,height=640,width=640"
			}
			requestURL := serverURL + "/i/" + sourceHash + "/" + url.PathEscape(rawSpec) + ".webp"
			results[index] = issueRequest(config, item, client, requestURL, trialID, hostname, fmt.Sprintf("request-%03d", index+1))
		}()
	}
	ready.Wait()
	close(start)
	finished.Wait()
	return results
}

func issueRecoveryRequest(config Config, item scheduledScenario, serverURL, sourceHash, trialID, hostname string) RequestResult {
	client := &http.Client{}
	defer client.CloseIdleConnections()
	rawSpec := "width=640,height=640,fit=cover,quality=80"
	requestURL := serverURL + "/i/" + sourceHash + "/" + url.PathEscape(rawSpec) + ".webp"
	return issueRequest(config, item, client, requestURL, trialID, hostname, "recovery-001")
}

func issueRequest(config Config, item scheduledScenario, client *http.Client, requestURL, trialID, hostname, requestID string) RequestResult {
	started := time.Now()
	result := RequestResult{
		RunID:     config.RunID,
		TrialID:   trialID,
		Scenario:  item.ID,
		RequestID: requestID,
		TaskID:    hostname,
		StartedAt: started.UTC().Format(time.RFC3339Nano),
	}
	ctx, cancel := context.WithTimeout(context.Background(), config.RequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		result.ErrorType = "request_build"
		return finishRequest(result, started)
	}
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			result.ErrorType = "timeout"
		} else if errors.Is(err, context.Canceled) {
			result.ErrorType = "canceled"
		} else {
			result.ErrorType = "transport"
		}
		return finishRequest(result, started)
	}
	defer response.Body.Close()
	data, readErr := io.ReadAll(response.Body)
	result.HTTPStatus = response.StatusCode
	result.ImageKey = response.Header.Get("X-Derivative-Key")
	result.Coalesced = response.Header.Get("X-Request-Coalesced") == "true"
	result.Cache = response.Header.Get("X-Media-Cache")
	if readErr != nil {
		result.ErrorType = "response_read"
	} else if response.StatusCode != http.StatusOK {
		result.ErrorType = "http_" + strconv.Itoa(response.StatusCode)
	} else {
		digest := sha256.Sum256(data)
		result.ResponseSHA256 = hex.EncodeToString(digest[:])
	}
	return finishRequest(result, started)
}

func finishRequest(result RequestResult, started time.Time) RequestResult {
	finished := time.Now()
	result.FinishedAt = finished.UTC().Format(time.RFC3339Nano)
	result.LatencyMS = milliseconds(finished.Sub(started))
	return result
}

func summarizeTrial(
	config Config,
	item scheduledScenario,
	trialID string,
	requests []RequestResult,
	resources []ResourceSample,
	snapshot media.MetricsSnapshot,
) TrialResult {
	trial := TrialResult{
		RunID:             config.RunID,
		TrialID:           trialID,
		ColdStateID:       trialID,
		Scenario:          item.ID,
		Mode:              item.Mode,
		Repetition:        item.repetition,
		Concurrency:       item.Concurrency,
		Valid:             true,
		TotalRequests:     len(requests),
		TransformAttempts: snapshot.TransformSuccess + snapshot.TransformError + snapshot.TransformTimeout,
		OriginalReads:     snapshot.OriginalSuccess + snapshot.OriginalError,
		PublishCreated:    snapshot.PublishCreated,
		PublishExisting:   snapshot.PublishExisting,
		CoalescedRequests: snapshot.Coalesced,
	}

	starts := make([]time.Time, 0, item.Concurrency)
	latencies := make([]float64, 0, len(requests))
	responseHashes := make(map[string]bool)
	for _, request := range requests {
		if request.RequestID != "recovery-001" {
			if started, err := time.Parse(time.RFC3339Nano, request.StartedAt); err == nil {
				starts = append(starts, started)
			}
		}
		latencies = append(latencies, request.LatencyMS)
		if request.HTTPStatus == http.StatusOK && request.ErrorType == "" {
			trial.SuccessRequests++
			responseHashes[request.ResponseSHA256] = true
		} else {
			trial.ErrorRequests++
		}
	}
	if len(starts) > 0 {
		sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })
		trial.StartSkewMS = milliseconds(starts[len(starts)-1].Sub(starts[0]))
	}
	sort.Float64s(latencies)
	trial.P50MS = percentile(latencies, 0.50)
	trial.P95MS = percentile(latencies, 0.95)
	trial.P99MS = percentile(latencies, 0.99)

	if config.StartSkewLimit > 0 && time.Duration(trial.StartSkewMS*float64(time.Millisecond)) > config.StartSkewLimit {
		invalidate(&trial, "start_skew_exceeded")
	}
	if item.Failure {
		if len(requests) != item.Concurrency+1 || requests[len(requests)-1].HTTPStatus != http.StatusOK {
			invalidate(&trial, "failure_recovery_failed")
		}
		for _, request := range requests[:len(requests)-1] {
			if request.HTTPStatus != http.StatusInternalServerError {
				invalidate(&trial, "waiter_did_not_receive_leader_error")
				break
			}
		}
	} else {
		if trial.SuccessRequests != item.Concurrency || len(responseHashes) != 1 {
			invalidate(&trial, "request_or_output_mismatch")
		}
		if item.Mode == media.CoordinatorProcessSingle && trial.TransformAttempts != 1 {
			invalidate(&trial, "singleflight_transform_count")
		}
	}

	if len(resources) > 0 {
		firstCPU := resources[0].CPUUsageUsec
		lastCPU := resources[len(resources)-1].CPUUsageUsec
		if firstCPU >= 0 && lastCPU >= firstCPU {
			trial.CPUTimeMS = float64(lastCPU-firstCPU) / 1000
		}
		for _, sample := range resources {
			trial.PeakRSSBytes = max(trial.PeakRSSBytes, sample.RSSBytes)
			trial.PeakCgroupMemBytes = max(trial.PeakCgroupMemBytes, sample.CgroupMemoryBytes)
		}
	}
	return trial
}

func invalidate(trial *TrialResult, reason string) {
	trial.Valid = false
	if trial.InvalidReason == "" {
		trial.InvalidReason = reason
	} else if !strings.Contains(trial.InvalidReason, reason) {
		trial.InvalidReason += ";" + reason
	}
}

func percentile(sorted []float64, quantile float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	index := int(float64(len(sorted)-1) * quantile)
	return sorted[index]
}

func warmUp(transformer media.Transformer, fixture []byte) error {
	spec, err := media.ParseTransformSpec("width=32,height=32,fit=cover,quality=80", "webp")
	if err != nil {
		return err
	}
	_, err = transformer.Transform(context.Background(), fixture, spec)
	return err
}

func validateConfig(config Config) error {
	if config.RunID == "" || strings.ContainsAny(config.RunID, `/\\`) {
		return errors.New("run ID must be a non-empty path segment")
	}
	if config.Repetitions < 1 {
		return errors.New("repetitions must be at least one")
	}
	if config.TransformConcurrency < 1 {
		return errors.New("transform concurrency must be at least one")
	}
	if config.RequestTimeout <= 0 || config.TransformTimeout <= 0 || config.ResourceSampleGap <= 0 {
		return errors.New("timeouts and resource sample gap must be positive")
	}
	return nil
}
