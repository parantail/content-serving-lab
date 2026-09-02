package e1phaseb

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

const (
	hotRawSpec       = "width=640,height=640,fit=cover,quality=80"
	unrelatedRawSpec = "width=640,height=640,fit=cover,quality=79"
	formatWebP       = "webp"
)

type scenario struct {
	ID                string
	Concurrency       int
	ProcessCount      int
	HotRequests       int
	UnrelatedRequests int
	WarmUnrelated     bool
	Cancellation      bool
	Repetition        int
}

func Execute(config Config) (RunOutput, error) {
	if err := validateConfig(config); err != nil {
		return RunOutput{}, err
	}
	fixture, err := os.ReadFile(config.FixturePath)
	if err != nil {
		return RunOutput{}, fmt.Errorf("read fixture: %w", err)
	}
	digest := sha256.Sum256(fixture)
	sourceHash := hex.EncodeToString(digest[:])
	hotSpec, err := media.ParseTransformSpec(hotRawSpec, formatWebP)
	if err != nil {
		return RunOutput{}, err
	}
	unrelatedSpec, err := media.ParseTransformSpec(unrelatedRawSpec, formatWebP)
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
	metadata.HotCanonicalSpec = hotSpec.Canonical()
	metadata.UnrelatedCanonicalSpec = unrelatedSpec.Canonical()
	metadata.CPUQuota = readCPUQuota()
	metadata.MemoryLimitBytes = readMemoryLimit()
	metadata.Commands = []string{strings.Join(config.Command, " ")}
	metadata.Environment["coordinator_mode"] = media.CoordinatorProcessSingle
	metadata.Environment["hot_unrelated_ratio"] = "90:10"
	metadata.Environment["local_s4_routing"] = "deterministic-round-robin"
	metadata.Environment["libvips_concurrency"] = "1"
	metadata.Environment["libvips_operation_cache"] = "disabled"
	metadata.Environment["max_concurrent_transforms_per_process"] = strconv.Itoa(config.TransformConcurrency)
	metadata.KnownLimitations = []string{
		"The workload is synthetic HTTP traffic generated inside one aggregate container cgroup.",
		"S3 uses two derivative keys of one source and does not measure isolation between different source objects.",
		"Local S4 uses direct deterministic round-robin routing and a shared filesystem; it does not emulate ALB or S3.",
		"govips cannot interrupt a native libvips call already executing in C.",
	}

	directory := filepath.Join(config.ResultsRoot, config.RunID)
	if _, err := os.Stat(directory); err == nil {
		return RunOutput{}, fmt.Errorf("result directory already exists: %s", directory)
	} else if !errors.Is(err, os.ErrNotExist) {
		return RunOutput{}, err
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return RunOutput{}, err
	}

	output := RunOutput{Directory: directory, Metadata: metadata}
	for _, item := range buildSchedule(config.Repetitions) {
		trial, requests, resources, metrics, events, err := executeIsolatedTrial(config, item)
		if err != nil {
			return RunOutput{}, fmt.Errorf("execute %s repetition %d: %w", item.ID, item.Repetition, err)
		}
		output.Trials = append(output.Trials, trial)
		output.Requests = append(output.Requests, requests...)
		output.Resources = append(output.Resources, resources...)
		output.Metrics = append(output.Metrics, metrics...)
		output.Events = append(output.Events, events...)
		output.Metadata.ExecutionOrder = append(output.Metadata.ExecutionOrder, trial.TrialID)
		fmt.Fprintf(os.Stderr, "trial_finished=%s valid=%t transforms=%d\n", trial.TrialID, trial.Valid, trial.TransformAttempts)
	}
	if err := writeRunOutput(output); err != nil {
		return RunOutput{}, err
	}
	return output, nil
}

func buildSchedule(repetitions int) []scenario {
	schedule := make([]scenario, 0, repetitions*7)
	for repetition := 1; repetition <= repetitions; repetition++ {
		coldControl := scenario{ID: ScenarioS3ColdControl, Concurrency: 10, UnrelatedRequests: 10, Repetition: repetition}
		coldMixed := scenario{ID: ScenarioS3Cold, Concurrency: 100, HotRequests: 90, UnrelatedRequests: 10, Repetition: repetition}
		warmControl := scenario{ID: ScenarioS3WarmControl, Concurrency: 10, UnrelatedRequests: 10, WarmUnrelated: true, Repetition: repetition}
		warmMixed := scenario{ID: ScenarioS3Warm, Concurrency: 100, HotRequests: 90, UnrelatedRequests: 10, WarmUnrelated: true, Repetition: repetition}
		if repetition%2 == 1 {
			schedule = append(schedule, coldControl, coldMixed, warmControl, warmMixed)
		} else {
			schedule = append(schedule, coldMixed, coldControl, warmMixed, warmControl)
		}
		schedule = append(schedule, scenario{ID: ScenarioF2, Concurrency: 10, HotRequests: 10, Cancellation: true, Repetition: repetition})
		if repetition%2 == 1 {
			schedule = append(schedule,
				scenario{ID: ScenarioS42, Concurrency: 100, ProcessCount: 2, HotRequests: 100, Repetition: repetition},
				scenario{ID: ScenarioS44, Concurrency: 100, ProcessCount: 4, HotRequests: 100, Repetition: repetition},
			)
		} else {
			schedule = append(schedule,
				scenario{ID: ScenarioS44, Concurrency: 100, ProcessCount: 4, HotRequests: 100, Repetition: repetition},
				scenario{ID: ScenarioS42, Concurrency: 100, ProcessCount: 2, HotRequests: 100, Repetition: repetition},
			)
		}
	}
	return schedule
}

func executeTrial(config Config, item scenario, fixture []byte, sourceHash, hostname string) (TrialResult, []RequestResult, []ResourceSample, []MetricRecord, []Event, error) {
	if item.ProcessCount > 0 {
		return executeMultiProcessTrial(config, item, sourceHash, hostname)
	}
	if item.Cancellation {
		return executeCancellationTrial(config, item, fixture, sourceHash, hostname)
	}
	return executeIsolationTrial(config, item, fixture, sourceHash, hostname)
}

func executeIsolationTrial(config Config, item scenario, fixture []byte, sourceHash, hostname string) (TrialResult, []RequestResult, []ResourceSample, []MetricRecord, []Event, error) {
	trialID := trialID(item)
	directory, err := os.MkdirTemp("", "content-serving-e1-phase-b-"+trialID+"-")
	if err != nil {
		return TrialResult{}, nil, nil, nil, nil, err
	}
	defer os.RemoveAll(directory)
	originalPath := filepath.Join(directory, "original.jpg")
	if err := os.WriteFile(originalPath, fixture, 0o644); err != nil {
		return TrialResult{}, nil, nil, nil, nil, err
	}
	derivativeDirectory := filepath.Join(directory, "derivatives")
	derivatives, err := media.NewLocalDerivativeStore(derivativeDirectory)
	if err != nil {
		return TrialResult{}, nil, nil, nil, nil, err
	}
	transformer := media.NewVipsTransformerWithConcurrency(config.TransformConcurrency)
	if err := warmUp(transformer, fixture); err != nil {
		return TrialResult{}, nil, nil, nil, nil, fmt.Errorf("warm up transformer: %w", err)
	}
	originals := media.NewFileOriginalStore(map[string]string{sourceHash: originalPath})
	if item.WarmUnrelated {
		coordinator := media.NewProcessCoordinator(config.TransformTimeout)
		prewarm := media.NewProcessor(originals, derivatives, transformer, coordinator, &media.Metrics{})
		spec, parseErr := media.ParseTransformSpec(unrelatedRawSpec, formatWebP)
		if parseErr != nil {
			return TrialResult{}, nil, nil, nil, nil, parseErr
		}
		ctx, cancel := context.WithTimeout(context.Background(), config.RequestTimeout)
		_, err = prewarm.GetDerivative(ctx, sourceHash, spec)
		cancel()
		if err != nil {
			return TrialResult{}, nil, nil, nil, nil, fmt.Errorf("prewarm unrelated derivative: %w", err)
		}
	}

	metrics := &media.Metrics{}
	coordinator := media.NewProcessCoordinator(config.TransformTimeout)
	processor := media.NewProcessor(originals, derivatives, transformer, coordinator, metrics)
	taskID := "process-01"
	server := httptest.NewServer(withTaskID(httpapi.NewHandler(processor), taskID))
	defer server.Close()

	sampler := startResourceSampler(config.RunID, trialID, config.ResourceSampleGap)
	requests := issueIsolationBurst(config, item, server.URL, sourceHash, trialID, hostname, taskID)
	resources := sampler.finish()
	metricRecords := []MetricRecord{{TrialID: trialID, Scenario: item.ID, TaskID: taskID, Snapshot: metrics.Snapshot()}}
	trial := summarizeTrial(config, item, trialID, requests, resources, metricRecords, countDerivativeFiles(derivativeDirectory))
	return trial, requests, resources, metricRecords, nil, nil
}

func issueIsolationBurst(config Config, item scenario, serverURL, sourceHash, trialID, hostname, taskID string) []RequestResult {
	client := burstClient(item.Concurrency)
	defer client.CloseIdleConnections()
	requests := make([]RequestResult, item.Concurrency)
	start := make(chan struct{})
	var ready, finished sync.WaitGroup
	ready.Add(item.Concurrency)
	finished.Add(item.Concurrency)
	for index := 0; index < item.Concurrency; index++ {
		index := index
		go func() {
			defer finished.Done()
			class := RequestClassUnrelated
			rawSpec := unrelatedRawSpec
			if item.HotRequests > 0 && index%10 != 9 {
				class = RequestClassHot
				rawSpec = hotRawSpec
			}
			ready.Done()
			<-start
			requestURL := derivativeURL(serverURL, sourceHash, rawSpec)
			requests[index] = issueRequest(config, item, client, requestURL, trialID, fmt.Sprintf("request-%03d", index+1), class, RequestRoleBurst, taskID, hostname)
		}()
	}
	ready.Wait()
	close(start)
	finished.Wait()
	return requests
}

func executeCancellationTrial(config Config, item scenario, fixture []byte, sourceHash, hostname string) (TrialResult, []RequestResult, []ResourceSample, []MetricRecord, []Event, error) {
	trialID := trialID(item)
	directory, err := os.MkdirTemp("", "content-serving-e1-phase-b-"+trialID+"-")
	if err != nil {
		return TrialResult{}, nil, nil, nil, nil, err
	}
	defer os.RemoveAll(directory)
	originalPath := filepath.Join(directory, "original.jpg")
	if err := os.WriteFile(originalPath, fixture, 0o644); err != nil {
		return TrialResult{}, nil, nil, nil, nil, err
	}
	derivativeDirectory := filepath.Join(directory, "derivatives")
	derivatives, err := media.NewLocalDerivativeStore(derivativeDirectory)
	if err != nil {
		return TrialResult{}, nil, nil, nil, nil, err
	}
	transformer := &media.DeterministicTransformer{Delay: 500 * time.Millisecond, Output: []byte("phase-b-deterministic-webp")}
	coordinator := media.NewProcessCoordinator(config.TransformTimeout)
	metrics := &media.Metrics{}
	processor := media.NewProcessor(
		media.NewFileOriginalStore(map[string]string{sourceHash: originalPath}),
		derivatives,
		transformer,
		coordinator,
		metrics,
	)
	taskID := "process-01"
	server := httptest.NewServer(withTaskID(httpapi.NewHandler(processor), taskID))
	defer server.Close()
	client := burstClient(item.Concurrency)
	defer client.CloseIdleConnections()

	events := make([]Event, 0, 6)
	recordEvent := func(name, requestID, detail string) {
		events = append(events, Event{RunID: config.RunID, TrialID: trialID, Scenario: item.ID, Name: name, RequestID: requestID, Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Detail: detail})
	}
	sampler := startResourceSampler(config.RunID, trialID, config.ResourceSampleGap)
	requestURL := derivativeURL(server.URL, sourceHash, hotRawSpec)
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	defer cancelLeader()
	leaderResult := make(chan RequestResult, 1)
	go func() {
		leaderResult <- issueRequestWithContext(item, client, leaderCtx, requestURL, config.RunID, trialID, "request-001", RequestClassHot, RequestRoleLeader, taskID, hostname)
	}()
	if err := waitUntil(2*time.Second, func() bool { return transformer.Calls() == 1 }); err != nil {
		_ = sampler.finish()
		return TrialResult{}, nil, nil, nil, nil, fmt.Errorf("wait for leader transform: %w", err)
	}
	recordEvent("leader_transform_started", "request-001", "deterministic transformer call 1")

	waiters := make([]RequestResult, item.Concurrency-1)
	startWaiters := make(chan struct{})
	var ready, finished sync.WaitGroup
	ready.Add(len(waiters))
	finished.Add(len(waiters))
	for index := range waiters {
		index := index
		go func() {
			defer finished.Done()
			ready.Done()
			<-startWaiters
			waiters[index] = issueRequest(config, item, client, requestURL, trialID, fmt.Sprintf("request-%03d", index+2), RequestClassHot, RequestRoleWaiter, taskID, hostname)
		}()
	}
	ready.Wait()
	close(startWaiters)
	if err := waitUntil(2*time.Second, func() bool {
		snapshot := coordinator.Snapshot()
		return snapshot.Keys == 1 && snapshot.Waiters == len(waiters)
	}); err != nil {
		_ = sampler.finish()
		return TrialResult{}, nil, nil, nil, nil, fmt.Errorf("wait for waiter registration: %w", err)
	}
	recordEvent("waiters_joined", "", strconv.Itoa(len(waiters)))
	cancelLeader()
	recordEvent("leader_cancel_sent", "request-001", "request context canceled after all waiters joined")
	leader := <-leaderResult
	recordEvent("leader_finished", "request-001", leader.ErrorType)
	finished.Wait()
	recordEvent("waiters_finished", "", strconv.Itoa(len(waiters)))
	followUp := issueRequest(config, item, client, requestURL, trialID, "follow-up-001", RequestClassFollowUp, RequestRoleFollowUp, taskID, hostname)
	recordEvent("follow_up_finished", "follow-up-001", followUp.Cache)
	resources := sampler.finish()

	requests := make([]RequestResult, 0, item.Concurrency+1)
	requests = append(requests, leader)
	requests = append(requests, waiters...)
	requests = append(requests, followUp)
	metricRecords := []MetricRecord{{TrialID: trialID, Scenario: item.ID, TaskID: taskID, Snapshot: metrics.Snapshot()}}
	trial := summarizeTrial(config, item, trialID, requests, resources, metricRecords, countDerivativeFiles(derivativeDirectory))
	if snapshot := coordinator.Snapshot(); snapshot.Keys != 0 || snapshot.Waiters != 0 {
		invalidate(&trial, "coordinator_not_drained")
	}
	return trial, requests, resources, metricRecords, events, nil
}

func issueRequest(config Config, item scenario, client *http.Client, requestURL, trialID, requestID, class, role, targetTaskID, fallbackTaskID string) RequestResult {
	ctx, cancel := context.WithTimeout(context.Background(), config.RequestTimeout)
	defer cancel()
	return issueRequestWithContext(item, client, ctx, requestURL, config.RunID, trialID, requestID, class, role, targetTaskID, fallbackTaskID)
}

func issueRequestWithContext(item scenario, client *http.Client, ctx context.Context, requestURL, runID, trialID, requestID, class, role, targetTaskID, fallbackTaskID string) RequestResult {
	started := time.Now()
	result := RequestResult{
		RunID: runID, TrialID: trialID, Scenario: item.ID, RequestID: requestID,
		RequestClass: class, RequestRole: role, TargetTaskID: targetTaskID,
		StartedAt: started.UTC().Format(time.RFC3339Nano),
	}
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
	result.TaskID = response.Header.Get("X-Task-ID")
	if result.TaskID == "" {
		result.TaskID = fallbackTaskID
	}
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

func summarizeTrial(config Config, item scenario, id string, requests []RequestResult, resources []ResourceSample, records []MetricRecord, derivativeFiles int) TrialResult {
	trial := TrialResult{
		RunID: config.RunID, TrialID: id, ColdStateID: id, Scenario: item.ID, Repetition: item.Repetition,
		Concurrency: item.Concurrency, ProcessCount: item.ProcessCount, HotRequests: item.HotRequests,
		UnrelatedRequests: item.UnrelatedRequests, WarmUnrelated: item.WarmUnrelated, Valid: true,
		TotalRequests: len(requests), DerivativeFiles: derivativeFiles,
	}
	starts := make([]time.Time, 0, item.Concurrency)
	latencies := make([]float64, 0, len(requests))
	activeTasks := make(map[string]bool)
	for _, request := range requests {
		if request.RequestRole != RequestRoleFollowUp {
			if started, err := time.Parse(time.RFC3339Nano, request.StartedAt); err == nil {
				starts = append(starts, started)
			}
		}
		latencies = append(latencies, request.LatencyMS)
		if request.ErrorType == "canceled" {
			trial.CanceledRequests++
		} else if request.HTTPStatus == http.StatusOK && request.ErrorType == "" {
			trial.SuccessRequests++
		} else {
			trial.ErrorRequests++
		}
		if request.TaskID != "" {
			activeTasks[request.TaskID] = true
		}
	}
	trial.ActiveTasks = len(activeTasks)
	if len(starts) > 1 {
		sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })
		trial.StartSkewMS = milliseconds(starts[len(starts)-1].Sub(starts[0]))
	}
	sort.Float64s(latencies)
	trial.P50MS = percentile(latencies, 0.50)
	trial.P95MS = percentile(latencies, 0.95)
	trial.P99MS = percentile(latencies, 0.99)
	for _, record := range records {
		snapshot := record.Snapshot
		trial.DerivativeHits += snapshot.DerivativeHits
		trial.DerivativeMisses += snapshot.DerivativeMisses
		trial.TransformAttempts += snapshot.TransformSuccess + snapshot.TransformError + snapshot.TransformTimeout
		trial.TransformMaxInflight = max(trial.TransformMaxInflight, snapshot.TransformMaxInflight)
		trial.TransformInflight += snapshot.TransformInflight
		trial.OriginalReads += snapshot.OriginalSuccess + snapshot.OriginalError
		trial.PublishCreated += snapshot.PublishCreated
		trial.PublishExisting += snapshot.PublishExisting
		trial.PublishErrors += snapshot.PublishError
		trial.CoalescedRequests += snapshot.Coalesced
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
	validateTrial(config, item, requests, &trial)
	return trial
}

func validateTrial(config Config, item scenario, requests []RequestResult, trial *TrialResult) {
	if !item.Cancellation && config.StartSkewLimit > 0 && time.Duration(trial.StartSkewMS*float64(time.Millisecond)) > config.StartSkewLimit {
		invalidate(trial, "start_skew_exceeded")
	}
	if trial.TransformInflight != 0 {
		invalidate(trial, "transform_not_drained")
	}
	if trial.PublishErrors != 0 {
		invalidate(trial, "publish_error")
	}

	switch item.ID {
	case ScenarioS3ColdControl:
		if trial.TotalRequests != 10 || trial.SuccessRequests != 10 || trial.TransformAttempts != 1 || trial.OriginalReads != 1 || trial.PublishCreated != 1 || trial.DerivativeFiles != 1 {
			invalidate(trial, "cold_control_contract")
		}
	case ScenarioS3Cold:
		if trial.TotalRequests != 100 || trial.SuccessRequests != 100 || trial.TransformAttempts != 2 || trial.OriginalReads != 2 || trial.PublishCreated != 2 || trial.DerivativeFiles != 2 {
			invalidate(trial, "cold_mixed_contract")
		}
		if trial.TransformMaxInflight < 2 {
			invalidate(trial, "different_keys_did_not_overlap")
		}
	case ScenarioS3WarmControl:
		if trial.TotalRequests != 10 || trial.SuccessRequests != 10 || trial.DerivativeHits != 10 || trial.TransformAttempts != 0 || trial.OriginalReads != 0 || trial.PublishCreated != 0 || trial.DerivativeFiles != 1 {
			invalidate(trial, "warm_control_contract")
		}
	case ScenarioS3Warm:
		if trial.TotalRequests != 100 || trial.SuccessRequests != 100 || trial.DerivativeHits != 10 || trial.TransformAttempts != 1 || trial.OriginalReads != 1 || trial.PublishCreated != 1 || trial.DerivativeFiles != 2 {
			invalidate(trial, "warm_mixed_contract")
		}
	case ScenarioF2:
		if trial.TotalRequests != 11 || trial.SuccessRequests != 10 || trial.CanceledRequests != 1 || trial.ErrorRequests != 0 || trial.TransformAttempts != 1 || trial.OriginalReads != 1 || trial.PublishCreated != 1 || trial.DerivativeHits != 1 || trial.DerivativeFiles != 1 || trial.CoalescedRequests != 9 {
			invalidate(trial, "cancellation_contract")
		}
		if len(requests) == 0 || requests[0].RequestRole != RequestRoleLeader || requests[0].ErrorType != "canceled" {
			invalidate(trial, "leader_not_canceled")
		}
		if requests[len(requests)-1].RequestRole != RequestRoleFollowUp || requests[len(requests)-1].Cache != "derivative" || requests[len(requests)-1].HTTPStatus != http.StatusOK {
			invalidate(trial, "follow_up_not_hit")
		}
	case ScenarioS42, ScenarioS44:
		if trial.TotalRequests != 100 || trial.SuccessRequests != 100 || trial.ActiveTasks != item.ProcessCount || trial.TransformAttempts < 1 || trial.TransformAttempts > int64(item.ProcessCount) || trial.PublishCreated != 1 || trial.PublishExisting != trial.TransformAttempts-1 || trial.DerivativeFiles != 1 {
			invalidate(trial, "multi_process_contract")
		}
	}
	if !successfulHashesMatchByClass(item, requests) {
		invalidate(trial, "response_hash_mismatch")
	}
}

func successfulHashesMatchByClass(item scenario, requests []RequestResult) bool {
	classHashes := make(map[string]map[string]bool)
	for _, request := range requests {
		if request.HTTPStatus != http.StatusOK || request.ErrorType != "" {
			continue
		}
		class := request.RequestClass
		if item.ID == ScenarioF2 && class == RequestClassFollowUp {
			class = RequestClassHot
		}
		if classHashes[class] == nil {
			classHashes[class] = make(map[string]bool)
		}
		classHashes[class][request.ResponseSHA256] = true
	}
	for _, hashes := range classHashes {
		if len(hashes) != 1 {
			return false
		}
	}
	return true
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
	return sorted[int(float64(len(sorted)-1)*quantile)]
}

func trialID(item scenario) string {
	return fmt.Sprintf("%s-r%02d", item.ID, item.Repetition)
}

func derivativeURL(serverURL, sourceHash, rawSpec string) string {
	return serverURL + "/i/" + sourceHash + "/" + url.PathEscape(rawSpec) + ".webp"
}

func burstClient(concurrency int) *http.Client {
	return &http.Client{Transport: &http.Transport{
		MaxIdleConns: concurrency, MaxIdleConnsPerHost: concurrency, MaxConnsPerHost: concurrency,
	}}
}

func withTaskID(next http.Handler, taskID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Task-ID", taskID)
		next.ServeHTTP(w, r)
	})
}

func waitUntil(timeout time.Duration, condition func() bool) error {
	deadline := time.Now().Add(timeout)
	for !condition() {
		if time.Now().After(deadline) {
			return errors.New("condition not met before timeout")
		}
		time.Sleep(time.Millisecond)
	}
	return nil
}

func countDerivativeFiles(directory string) int {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return -1
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".tmp") {
			return -1
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".webp") {
			count++
		}
	}
	return count
}

func warmUp(transformer media.Transformer, fixture []byte) error {
	spec, err := media.ParseTransformSpec("width=32,height=32,fit=cover,quality=80", formatWebP)
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
	if config.Repetitions < 1 || config.TransformConcurrency < 1 {
		return errors.New("repetitions and transform concurrency must be positive")
	}
	if config.RequestTimeout <= 0 || config.TransformTimeout <= 0 || config.ResourceSampleGap <= 0 {
		return errors.New("timeouts and resource sample gap must be positive")
	}
	return nil
}
