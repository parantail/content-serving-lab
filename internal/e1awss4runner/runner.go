package e1awss4runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/parantail/content-serving-lab/internal/e1awss4"
	"github.com/parantail/content-serving-lab/internal/media"
	"golang.org/x/image/webp"
)

const responseBodyLimit = 64 << 20

func ValidateConfig(config Config) error {
	return validateConfig(config)
}

func Execute(ctx context.Context, config Config, dependencies Dependencies) (RunOutput, AnalysisOutput, error) {
	if err := ValidateConfig(config); err != nil {
		return RunOutput{}, AnalysisOutput{}, err
	}
	if dependencies.Storage == nil || dependencies.Probe == nil || dependencies.HTTPClient == nil {
		return RunOutput{}, AnalysisOutput{}, errors.New("AWS S4 runner dependencies are required")
	}

	schedule := buildSchedule(config)
	directory := filepath.Join(config.ResultsRoot, config.RunID)
	if err := os.MkdirAll(config.ResultsRoot, 0o755); err != nil {
		return RunOutput{}, AnalysisOutput{}, fmt.Errorf("create results root: %w", err)
	}
	if err := os.Mkdir(directory, 0o755); err != nil {
		return RunOutput{}, AnalysisOutput{}, fmt.Errorf("create result directory: %w", err)
	}
	output := RunOutput{
		Directory: directory,
		Metadata: RunMetadata{
			SchemaVersion: SchemaVersion, RunID: config.RunID, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
			Calibration: config.Calibration, GitCommit: config.GitCommit, ContainerDigest: config.ContainerDigest,
			Region: config.Region, SourceHash: config.SourceHash, CanonicalSpec: config.CanonicalSpec,
			DerivativeKey: config.DerivativeKey, RequestsPerTrial: RequestsPerTrial, Repetitions: config.Repetitions,
			RequestTimeoutMS: milliseconds(config.RequestTimeout), ControlTimeoutMS: milliseconds(config.ControlTimeout),
			ControlPollGapMS: milliseconds(config.ControlPollGap), StartSkewLimitMS: milliseconds(config.StartSkewLimit),
			ExpectedWebPBytes: config.ExpectedWebPBytes, SanitizationApplied: true,
			KnownLimitations: []string{
				"synthetic traffic sent from one Fargate load-generator task",
				"50 ms service resource sampling remains subject to calibration",
				"cost.json contains measured usage only until pricing and AWS billing data are added",
			},
		},
		Infrastructure: Infrastructure{SchemaVersion: SchemaVersion, RunID: config.RunID, Region: config.Region},
		Cost: CostDocument{
			SchemaVersion: SchemaVersion, RunID: config.RunID, Status: "usage-only",
			Note: "Pricing, ALB LCU, Fargate duration, log bytes, public IPv4 time, ECR storage and confirmed billing are added after AWS measurement.",
		},
	}
	for _, target := range config.Services {
		output.Infrastructure.Services = append(output.Infrastructure.Services, InfrastructureService{
			Scenario: target.Scenario, ExpectedTasks: target.ExpectedTasks, ListenerPort: target.ListenerPort,
			TargetAlgorithm: "round_robin", Stickiness: false,
		})
	}
	tasks := make(map[string]e1awss4.TaskIdentity)

	for _, item := range schedule {
		output.Metadata.ExecutionOrder = append(output.Metadata.ExecutionOrder, item.Scenario)
		trial, requests, taskRecords, resources, storage, events, err := executeTrial(ctx, config, item, dependencies)
		if err != nil {
			_ = writeRunOutput(output)
			return output, AnalysisOutput{}, fmt.Errorf("trial %s: %w", trialID(config.RunID, item), err)
		}
		output.Trials = append(output.Trials, trial)
		output.Requests = append(output.Requests, requests...)
		output.Tasks = append(output.Tasks, taskRecords...)
		output.Resources = append(output.Resources, resources...)
		output.Storage = append(output.Storage, storage...)
		output.Events = append(output.Events, events...)
		for _, record := range taskRecords {
			tasks[record.Task.TaskID] = record.Task
		}
		output.Cost = buildCost(config.RunID, output.Tasks)
		if err := writeRunOutput(output); err != nil {
			return output, AnalysisOutput{}, err
		}
	}
	for _, task := range tasks {
		output.Infrastructure.Tasks = append(output.Infrastructure.Tasks, task)
	}
	enforceRunTaskSets(output.Trials)
	sort.Slice(output.Infrastructure.Tasks, func(i, j int) bool {
		return output.Infrastructure.Tasks[i].TaskID < output.Infrastructure.Tasks[j].TaskID
	})
	if err := writeRunOutput(output); err != nil {
		return output, AnalysisOutput{}, err
	}
	// Preserve raw evidence before analysis can reject it. Failed analysis
	// must not strand the only copy in a stopped Fargate task.
	if err := uploadDirectory(ctx, config, dependencies.Storage, output.Directory); err != nil {
		return output, AnalysisOutput{}, err
	}
	analysis, err := Analyze(output.Directory)
	if err != nil {
		return output, AnalysisOutput{}, fmt.Errorf("analyze AWS S4 result: %w", err)
	}
	if err := uploadDirectory(ctx, config, dependencies.Storage, output.Directory); err != nil {
		return output, analysis, err
	}
	return output, analysis, nil
}

func enforceRunTaskSets(trials []TrialRecord) {
	baseline := make(map[string][]string)
	changed := make(map[string]bool)
	for _, trial := range trials {
		if existing, ok := baseline[trial.Scenario]; ok {
			if !sameStrings(existing, trial.HealthyBeforeTaskIDs) {
				changed[trial.Scenario] = true
			}
			continue
		}
		baseline[trial.Scenario] = sortedStrings(trial.HealthyBeforeTaskIDs)
	}
	for index := range trials {
		if changed[trials[index].Scenario] {
			invalidate(&trials[index], "service_task_set_changed")
		}
	}
}

func executeTrial(ctx context.Context, config Config, target ServiceTarget, dependencies Dependencies) (TrialRecord, []RequestRecord, []TaskRecord, []ResourceRecord, []StorageRecord, []EventRecord, error) {
	id := trialID(config.RunID, target)
	events := []EventRecord{newEvent(config, target, id, "trial_started", "")}
	before, err := dependencies.Probe.Snapshot(ctx, target)
	if err != nil {
		return TrialRecord{}, nil, nil, nil, nil, events, err
	}
	events = append(events, newEvent(config, target, id, "health_before", fmt.Sprintf("stable=%t healthy=%d", before.Stable, len(before.HealthyTaskIDs))))
	if !before.Stable || before.DesiredTasks != target.ExpectedTasks || before.RunningTasks != target.ExpectedTasks || before.PendingTasks != 0 || len(before.HealthyTaskIDs) != target.ExpectedTasks {
		return TrialRecord{}, nil, nil, nil, nil, events, errors.New("service is not stable at the pre-trial gate")
	}

	prepared, err := collectTaskReports(ctx, config, target, id, "prepare", nil, dependencies.HTTPClient)
	if err != nil {
		return TrialRecord{}, nil, nil, nil, nil, events, err
	}
	preparedIDs := sortedReportIDs(prepared)
	for _, report := range prepared {
		if err := validateTaskIdentity(config, report.Task); err != nil {
			return TrialRecord{}, nil, nil, nil, nil, events, err
		}
	}
	events = append(events, newEvent(config, target, id, "tasks_prepared", fmt.Sprintf("tasks=%d", len(preparedIDs))))

	objectKey := media.S3DerivativeObjectPrefix + config.DerivativeKey + ".webp"
	if err := dependencies.Storage.DeleteDerivative(ctx, config.DerivativeBucket, objectKey); err != nil {
		return TrialRecord{}, nil, nil, nil, nil, events, err
	}
	events = append(events, newEvent(config, target, id, "derivative_deleted", "result=success"))
	exists, err := dependencies.Storage.DerivativeExists(ctx, config.DerivativeBucket, objectKey)
	if err != nil {
		return TrialRecord{}, nil, nil, nil, nil, events, err
	}
	coldConfirmed := !exists
	events = append(events, newEvent(config, target, id, "cold_checked", fmt.Sprintf("missing=%t", coldConfirmed)))
	if !coldConfirmed {
		return TrialRecord{}, nil, nil, nil, nil, events, errors.New("derivative object still exists after delete")
	}

	requests, burstEvents := executeBurst(ctx, config, target, id, dependencies.HTTPClient)
	events = append(events, burstEvents...)
	identityByTask := make(map[string]e1awss4.TaskIdentity, len(prepared))
	for taskID, report := range prepared {
		identityByTask[taskID] = report.Task
	}
	for index := range requests {
		requests[index].AvailabilityZone = identityByTask[requests[index].TaskID].AvailabilityZone
	}

	finished, err := collectTaskReports(ctx, config, target, id, "finish", preparedIDs, dependencies.HTTPClient)
	if err != nil {
		return TrialRecord{}, requests, nil, nil, nil, events, err
	}
	finishedIDs := sortedReportIDs(finished)
	for taskID, report := range finished {
		if report.Task != prepared[taskID].Task {
			return TrialRecord{}, requests, nil, nil, nil, events, fmt.Errorf("task identity changed between prepare and finish for %s", taskID)
		}
	}
	events = append(events, newEvent(config, target, id, "tasks_finished", fmt.Sprintf("tasks=%d", len(finishedIDs))))

	stored, err := dependencies.Storage.ReadDerivative(ctx, config.DerivativeBucket, objectKey)
	if err != nil {
		return TrialRecord{}, requests, nil, nil, nil, events, err
	}
	storedDigest := sha256.Sum256(stored)
	storedHash := hex.EncodeToString(storedDigest[:])
	storedWebPValid := validWebP(stored)
	events = append(events, newEvent(config, target, id, "derivative_verified", fmt.Sprintf("bytes=%d webp=%t", len(stored), storedWebPValid)))

	after, err := dependencies.Probe.Snapshot(ctx, target)
	if err != nil {
		return TrialRecord{}, requests, nil, nil, nil, events, err
	}
	events = append(events, newEvent(config, target, id, "health_after", fmt.Sprintf("stable=%t healthy=%d", after.Stable, len(after.HealthyTaskIDs))))

	taskRecords, resources, storage := flattenTaskReports(config, target, id, finished)
	storage = append(storage,
		StorageRecord{RunID: config.RunID, TrialID: id, Scenario: target.Scenario, Actor: "load-generator", Operation: "delete_object", Result: "success", Requests: 1},
		StorageRecord{RunID: config.RunID, TrialID: id, Scenario: target.Scenario, Actor: "load-generator", Operation: "head_object", Result: map[bool]string{true: "missing", false: "found"}[coldConfirmed], Requests: 1},
		StorageRecord{RunID: config.RunID, TrialID: id, Scenario: target.Scenario, Actor: "load-generator", Operation: "get_object", Result: "success", Requests: 1, Bytes: int64(len(stored))},
	)
	trial := summarizeTrial(config, target, before, after, preparedIDs, finishedIDs, coldConfirmed, storedHash, int64(len(stored)), storedWebPValid, requests, taskRecords)
	return trial, requests, taskRecords, resources, storage, events, nil
}

func collectTaskReports(ctx context.Context, config Config, target ServiceTarget, trialID, action string, expectedIDs []string, client HTTPDoer) (map[string]e1awss4.TrialReport, error) {
	controlCtx, cancel := context.WithTimeout(ctx, config.ControlTimeout)
	defer cancel()
	reports := make(map[string]e1awss4.TrialReport)
	expected := make(map[string]bool, len(expectedIDs))
	for _, taskID := range expectedIDs {
		expected[taskID] = true
	}
	for len(reports) < target.ExpectedTasks {
		report, retry, err := requestTaskReport(controlCtx, target.Endpoint, trialID, action, client)
		if err != nil {
			return nil, err
		}
		if !retry {
			if action == "prepare" && report.State != "prepared" {
				return nil, fmt.Errorf("prepare returned state %q", report.State)
			}
			if action == "finish" && report.State != "finished" {
				return nil, fmt.Errorf("finish returned state %q", report.State)
			}
			if len(expected) > 0 && !expected[report.Task.TaskID] {
				return nil, fmt.Errorf("%s reached unexpected task %q", action, report.Task.TaskID)
			}
			reports[report.Task.TaskID] = report
		}
		if len(reports) == target.ExpectedTasks {
			break
		}
		select {
		case <-controlCtx.Done():
			return nil, fmt.Errorf("%s did not reach %d tasks: %w", action, target.ExpectedTasks, controlCtx.Err())
		case <-time.After(config.ControlPollGap):
		}
	}
	return reports, nil
}

func requestTaskReport(ctx context.Context, endpoint, trialID, action string, client HTTPDoer) (e1awss4.TrialReport, bool, error) {
	requestURL := strings.TrimRight(endpoint, "/") + "/internal/e1/trials/" + url.PathEscape(trialID) + "/" + action
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, nil)
	if err != nil {
		return e1awss4.TrialReport{}, false, err
	}
	response, err := client.Do(request)
	if err != nil {
		return e1awss4.TrialReport{}, false, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusConflict {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return e1awss4.TrialReport{}, true, nil
	}
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return e1awss4.TrialReport{}, false, fmt.Errorf("%s endpoint returned HTTP %d: %s", action, response.StatusCode, strings.TrimSpace(string(body)))
	}
	var report e1awss4.TrialReport
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return e1awss4.TrialReport{}, false, err
	}
	if report.Schema != e1awss4.SchemaVersion || report.TrialID != trialID || report.Task.TaskID == "" {
		return e1awss4.TrialReport{}, false, errors.New("control endpoint returned mismatched trial or empty task ID")
	}
	if response.Header.Get(e1awss4.TaskIDHeader) != report.Task.TaskID || response.Header.Get(e1awss4.TrialIDHeader) != trialID {
		return e1awss4.TrialReport{}, false, errors.New("control endpoint headers do not match its report")
	}
	return report, false, nil
}

func validateTaskIdentity(config Config, identity e1awss4.TaskIdentity) error {
	if identity.TaskID == "" || strings.ContainsAny(identity.TaskID, "/:") || identity.TaskDefinitionFamily == "" || identity.TaskDefinitionRevision == "" ||
		!strings.HasPrefix(identity.AvailabilityZone, config.Region) || identity.LaunchType != "FARGATE" || identity.CPUVCpu != 1 || identity.MemoryMiB != 2048 || identity.ImageDigest != config.ContainerDigest {
		return fmt.Errorf("task identity %q does not match the fixed AWS S4 infrastructure contract", identity.TaskID)
	}
	return nil
}

func executeBurst(ctx context.Context, config Config, target ServiceTarget, trialID string, client HTTPDoer) ([]RequestRecord, []EventRecord) {
	rawSpec := strings.TrimSuffix(config.CanonicalSpec, ",format=webp")
	requestURL := strings.TrimRight(target.Endpoint, "/") + "/i/" + config.SourceHash + "/" + url.PathEscape(rawSpec) + ".webp"
	requests := make([]RequestRecord, RequestsPerTrial)
	start := make(chan struct{})
	var ready sync.WaitGroup
	var finished sync.WaitGroup
	ready.Add(RequestsPerTrial)
	finished.Add(RequestsPerTrial)
	for index := range RequestsPerTrial {
		go func(index int) {
			defer finished.Done()
			ready.Done()
			<-start
			requests[index] = issueImageRequest(ctx, config, target, trialID, fmt.Sprintf("request-%03d", index+1), requestURL, client)
		}(index)
	}
	ready.Wait()
	events := []EventRecord{newEvent(config, target, trialID, "burst_ready", fmt.Sprintf("requests=%d", RequestsPerTrial))}
	close(start)
	events = append(events, newEvent(config, target, trialID, "burst_released", "start_channel=closed"))
	finished.Wait()
	events = append(events, newEvent(config, target, trialID, "burst_finished", fmt.Sprintf("requests=%d", RequestsPerTrial)))
	return requests, events
}

func issueImageRequest(parent context.Context, config Config, target ServiceTarget, trialID, requestID, requestURL string, client HTTPDoer) RequestRecord {
	started := time.Now().UTC()
	record := RequestRecord{
		RunID: config.RunID, TrialID: trialID, Scenario: target.Scenario, RequestID: requestID,
		StartedAt: started.Format(time.RFC3339Nano),
	}
	requestCtx, cancel := context.WithTimeout(parent, config.RequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, requestURL, nil)
	if err != nil {
		record.ErrorType = "request_build"
		return finishImageRequest(record, started)
	}
	request.Header.Set(e1awss4.TrialIDHeader, trialID)
	response, err := client.Do(request)
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			record.ErrorType = "timeout"
		case errors.Is(err, context.Canceled):
			record.ErrorType = "canceled"
		default:
			record.ErrorType = "connection"
		}
		return finishImageRequest(record, started)
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, responseBodyLimit+1))
	record.HTTPStatus = response.StatusCode
	record.TaskID = response.Header.Get(e1awss4.TaskIDHeader)
	record.TrialHeader = response.Header.Get(e1awss4.TrialIDHeader)
	record.DerivativeKey = response.Header.Get("X-Derivative-Key")
	record.Cache = response.Header.Get("X-Media-Cache")
	record.Coalesced = response.Header.Get("X-Request-Coalesced") == "true"
	record.ResponseBytes = int64(len(body))
	switch {
	case readErr != nil || len(body) > responseBodyLimit:
		record.ErrorType = "response_read"
	case response.StatusCode != http.StatusOK:
		record.ErrorType = "http_" + fmt.Sprint(response.StatusCode)
	case !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "image/webp"):
		record.ErrorType = "content_type"
	case record.TaskID == "":
		record.ErrorType = "missing_task_header"
	case record.TrialHeader != trialID:
		record.ErrorType = "trial_header_mismatch"
	default:
		digest := sha256.Sum256(body)
		record.ResponseSHA256 = hex.EncodeToString(digest[:])
	}
	return finishImageRequest(record, started)
}

func finishImageRequest(record RequestRecord, started time.Time) RequestRecord {
	finished := time.Now().UTC()
	record.FinishedAt = finished.Format(time.RFC3339Nano)
	record.LatencyMS = milliseconds(finished.Sub(started))
	return record
}

func flattenTaskReports(config Config, target ServiceTarget, trialID string, reports map[string]e1awss4.TrialReport) ([]TaskRecord, []ResourceRecord, []StorageRecord) {
	ids := sortedReportIDs(reports)
	var tasks []TaskRecord
	var resources []ResourceRecord
	var storage []StorageRecord
	for _, taskID := range ids {
		report := reports[taskID]
		tasks = append(tasks, TaskRecord{
			RunID: config.RunID, TrialID: trialID, Scenario: target.Scenario, Task: report.Task,
			PreparedAt: report.PreparedAt, FinishedAt: report.FinishedAt, FirstRequestAt: report.FirstRequestAt,
			LastRequestAt: report.LastRequestAt, Counters: report.Counters,
		})
		for _, sample := range report.Resources {
			resources = append(resources, ResourceRecord{
				RunID: config.RunID, TrialID: trialID, Scenario: target.Scenario, TaskID: taskID,
				Timestamp: sample.Timestamp, CPUUsageNanos: sample.CPUUsageNanos,
				MemoryUsageBytes: sample.MemoryUsageBytes, Error: sample.Error,
			})
		}
		counters := report.Counters
		storage = append(storage,
			StorageRecord{RunID: config.RunID, TrialID: trialID, Scenario: target.Scenario, Actor: "media-service", TaskID: taskID, Operation: "derivative_get", Result: "hit", Requests: counters.DerivativeGetHit, Bytes: counters.DerivativeGetBytes},
			StorageRecord{RunID: config.RunID, TrialID: trialID, Scenario: target.Scenario, Actor: "media-service", TaskID: taskID, Operation: "derivative_get", Result: "miss", Requests: counters.DerivativeGetMiss},
			StorageRecord{RunID: config.RunID, TrialID: trialID, Scenario: target.Scenario, Actor: "media-service", TaskID: taskID, Operation: "derivative_get", Result: "error", Requests: counters.DerivativeGetError},
			StorageRecord{RunID: config.RunID, TrialID: trialID, Scenario: target.Scenario, Actor: "media-service", TaskID: taskID, Operation: "original_get", Result: "success", Requests: counters.OriginalGetSuccess, Bytes: counters.OriginalGetBytes},
			StorageRecord{RunID: config.RunID, TrialID: trialID, Scenario: target.Scenario, Actor: "media-service", TaskID: taskID, Operation: "original_get", Result: "error", Requests: counters.OriginalGetError},
			StorageRecord{RunID: config.RunID, TrialID: trialID, Scenario: target.Scenario, Actor: "media-service", TaskID: taskID, Operation: "conditional_put", Result: "created", Requests: counters.PublishCreated},
			StorageRecord{RunID: config.RunID, TrialID: trialID, Scenario: target.Scenario, Actor: "media-service", TaskID: taskID, Operation: "conditional_put", Result: "existing", Requests: counters.PublishExisting},
			StorageRecord{RunID: config.RunID, TrialID: trialID, Scenario: target.Scenario, Actor: "media-service", TaskID: taskID, Operation: "conditional_put", Result: "conflict", Requests: counters.PublishConflict},
			StorageRecord{RunID: config.RunID, TrialID: trialID, Scenario: target.Scenario, Actor: "media-service", TaskID: taskID, Operation: "conditional_put", Result: "error", Requests: counters.PublishError},
			StorageRecord{RunID: config.RunID, TrialID: trialID, Scenario: target.Scenario, Actor: "media-service", TaskID: taskID, Operation: "conditional_put", Result: "attempt_bytes", Bytes: counters.PublishAttemptBytes},
		)
	}
	return tasks, resources, storage
}

func summarizeTrial(config Config, target ServiceTarget, before, after HealthSnapshot, preparedIDs, finishedIDs []string, coldConfirmed bool, storedHash string, storedBytes int64, storedWebPValid bool, requests []RequestRecord, tasks []TaskRecord) TrialRecord {
	trial := TrialRecord{
		RunID: config.RunID, TrialID: trialID(config.RunID, target), Scenario: target.Scenario, Repetition: targetRepetition(target),
		ExpectedTasks: target.ExpectedTasks, DeploymentStableBefore: before.Stable, DeploymentStableAfter: after.Stable,
		HealthyBeforeTaskIDs: sortedStrings(before.HealthyTaskIDs), HealthyAfterTaskIDs: sortedStrings(after.HealthyTaskIDs),
		PreparedTaskIDs: sortedStrings(preparedIDs), FinishedTaskIDs: sortedStrings(finishedIDs), ColdConfirmed: coldConfirmed,
		TotalRequests: len(requests), StoredSHA256: storedHash, StoredBytes: storedBytes, StoredWebPValid: storedWebPValid,
		Valid: true,
	}
	starts := make([]time.Time, 0, len(requests))
	latencies := make([]float64, 0, len(requests))
	hashes := make(map[string]bool)
	responseSizes := make(map[int64]bool)
	for _, request := range requests {
		if started, err := time.Parse(time.RFC3339Nano, request.StartedAt); err == nil {
			starts = append(starts, started)
		}
		latencies = append(latencies, request.LatencyMS)
		if request.HTTPStatus == http.StatusOK && request.ErrorType == "" {
			trial.SuccessRequests++
			hashes[request.ResponseSHA256] = true
			responseSizes[request.ResponseBytes] = true
		}
		if request.HTTPStatus >= 500 {
			trial.HTTP5xx++
		}
		switch request.ErrorType {
		case "timeout":
			trial.Timeouts++
		case "connection":
			trial.ConnectionErrors++
		}
	}
	if len(hashes) == 1 {
		for value := range hashes {
			trial.ResponseSHA256 = value
		}
	}
	if len(responseSizes) == 1 {
		for value := range responseSizes {
			trial.ResponseBytes = value
		}
	}
	if len(starts) > 1 {
		sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })
		trial.StartSkewMS = milliseconds(starts[len(starts)-1].Sub(starts[0]))
	}
	sort.Float64s(latencies)
	trial.P50MS = percentile(latencies, 0.50)
	trial.P95MS = percentile(latencies, 0.95)
	trial.P99MS = percentile(latencies, 0.99)
	for _, task := range tasks {
		counter := task.Counters
		trial.ImageRequests += counter.ImageRequests
		trial.DerivativeGetHit += counter.DerivativeGetHit
		trial.DerivativeGetMiss += counter.DerivativeGetMiss
		trial.DerivativeGetError += counter.DerivativeGetError
		trial.DerivativeGetBytes += counter.DerivativeGetBytes
		trial.OriginalGetCount += counter.OriginalGetCount
		trial.OriginalGetBytes += counter.OriginalGetBytes
		trial.TransformAttempts += counter.TransformAttempts
		trial.TransformSuccess += counter.TransformSuccess
		trial.TransformFailure += counter.TransformFailure
		trial.TransformDurationNanos += counter.TransformDurationNanos
		trial.TransformMaxInflight = max(trial.TransformMaxInflight, counter.TransformMaxInflight)
		trial.TransformInflight += counter.TransformInflight
		trial.CoalescedRequests += counter.CoalescedRequests
		trial.PublishCreated += counter.PublishCreated
		trial.PublishExisting += counter.PublishExisting
		trial.PublishConflict += counter.PublishConflict
		trial.PublishError += counter.PublishError
		trial.PublishAttemptBytes += counter.PublishAttemptBytes
		trial.CoordinatorKeys += counter.CoordinatorKeys
		trial.CoordinatorWaiters += counter.CoordinatorWaiters
		trial.CPUUsageNanos += counter.CPUUsageNanos
		trial.PeakMemoryBytes = max(trial.PeakMemoryBytes, counter.PeakMemoryBytes)
		trial.ResourceErrors += counter.ResourceErrors
	}
	validateTrial(config, target, requests, tasks, &trial)
	return trial
}

func validateTrial(config Config, target ServiceTarget, requests []RequestRecord, tasks []TaskRecord, trial *TrialRecord) {
	if !trial.DeploymentStableBefore || !trial.DeploymentStableAfter || !sameStrings(trial.HealthyBeforeTaskIDs, trial.HealthyAfterTaskIDs) || len(trial.HealthyBeforeTaskIDs) != target.ExpectedTasks {
		invalidate(trial, "task_set_not_stable")
	}
	if !sameStrings(trial.PreparedTaskIDs, trial.HealthyBeforeTaskIDs) || !sameStrings(trial.FinishedTaskIDs, trial.HealthyAfterTaskIDs) {
		invalidate(trial, "control_task_set_mismatch")
	}
	if !trial.ColdConfirmed {
		invalidate(trial, "cold_state_not_confirmed")
	}
	if trial.TotalRequests != RequestsPerTrial || trial.SuccessRequests != RequestsPerTrial || trial.HTTP5xx != 0 || trial.Timeouts != 0 || trial.ConnectionErrors != 0 {
		invalidate(trial, "request_contract")
	}
	if config.StartSkewLimit > 0 && time.Duration(trial.StartSkewMS*float64(time.Millisecond)) > config.StartSkewLimit {
		invalidate(trial, "start_skew_exceeded")
	}
	if trial.ResponseSHA256 == "" || trial.ResponseSHA256 != trial.StoredSHA256 || trial.ResponseBytes <= 0 || trial.ResponseBytes != trial.StoredBytes || !trial.StoredWebPValid {
		invalidate(trial, "stored_derivative_mismatch")
	}
	if config.ExpectedWebPBytes > 0 && trial.StoredBytes != config.ExpectedWebPBytes {
		invalidate(trial, "unexpected_derivative_size")
	}
	requestCounts := make(map[string]int64)
	for _, request := range requests {
		requestCounts[request.TaskID]++
		if request.DerivativeKey != config.DerivativeKey {
			invalidate(trial, "derivative_key_mismatch")
		}
	}
	if len(requestCounts) != target.ExpectedTasks || trial.ImageRequests != RequestsPerTrial {
		invalidate(trial, "task_request_distribution")
	}
	for _, task := range tasks {
		if task.Counters.ImageRequests != requestCounts[task.Task.TaskID] {
			invalidate(trial, "task_request_counter_mismatch")
		}
	}
	if trial.TransformAttempts < 1 || trial.TransformAttempts > int64(target.ExpectedTasks) || trial.TransformSuccess != trial.TransformAttempts || trial.TransformFailure != 0 || trial.OriginalGetCount != trial.TransformAttempts {
		invalidate(trial, "transform_counter_contract")
	}
	if trial.PublishCreated != 1 || trial.PublishExisting != trial.TransformAttempts-1 || trial.PublishConflict != 0 || trial.PublishError != 0 {
		invalidate(trial, "publish_counter_contract")
	}
	if trial.PublishAttemptBytes != trial.TransformAttempts*trial.ResponseBytes {
		invalidate(trial, "publish_byte_contract")
	}
	if trial.DerivativeGetError != 0 || trial.TransformInflight != 0 || trial.CoordinatorKeys != 0 || trial.CoordinatorWaiters != 0 || trial.ResourceErrors != 0 {
		invalidate(trial, "task_not_drained_or_counter_error")
	}
}

func buildSchedule(config Config) []ServiceTarget {
	byScenario := make(map[string]ServiceTarget, len(config.Services))
	for _, target := range config.Services {
		byScenario[target.Scenario] = target
	}
	var schedule []ServiceTarget
	for repetition := 1; repetition <= config.Repetitions; repetition++ {
		order := []string{ScenarioTask1, ScenarioTask2, ScenarioTask4}
		if repetition%2 == 0 {
			order = []string{ScenarioTask4, ScenarioTask2, ScenarioTask1}
		}
		for _, scenario := range order {
			target := byScenario[scenario]
			target.Repetition = repetition
			schedule = append(schedule, target)
		}
	}
	return schedule
}

func trialID(runID string, target ServiceTarget) string {
	return fmt.Sprintf("%s-%s-r%02d", runID, target.Scenario, targetRepetition(target))
}

func targetRepetition(target ServiceTarget) int {
	if target.Repetition < 1 {
		return 1
	}
	return target.Repetition
}

func newEvent(config Config, target ServiceTarget, trialID, name, detail string) EventRecord {
	return EventRecord{RunID: config.RunID, TrialID: trialID, Scenario: target.Scenario, Name: name, Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Detail: detail}
}

func sortedReportIDs(reports map[string]e1awss4.TrialReport) []string {
	ids := make([]string, 0, len(reports))
	for taskID := range reports {
		ids = append(ids, taskID)
	}
	sort.Strings(ids)
	return ids
}

func sortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	left, right = sortedStrings(left), sortedStrings(right)
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func percentile(sorted []float64, quantile float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[int(float64(len(sorted)-1)*quantile)]
}

func invalidate(trial *TrialRecord, reason string) {
	trial.Valid = false
	if trial.InvalidReason == "" {
		trial.InvalidReason = reason
	} else if !strings.Contains(trial.InvalidReason, reason) {
		trial.InvalidReason += ";" + reason
	}
}

func validWebP(data []byte) bool {
	if len(data) < 20 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return false
	}
	if uint64(binary.LittleEndian.Uint32(data[4:8]))+8 != uint64(len(data)) {
		return false
	}
	chunk := string(data[12:16])
	chunkSize := uint64(binary.LittleEndian.Uint32(data[16:20]))
	if (chunk != "VP8 " && chunk != "VP8L" && chunk != "VP8X") || chunkSize > uint64(len(data)-20) {
		return false
	}
	configuration, err := webp.DecodeConfig(bytes.NewReader(data))
	return err == nil && configuration.Width > 0 && configuration.Height > 0
}

func buildCost(runID string, tasks []TaskRecord) CostDocument {
	cost := CostDocument{
		SchemaVersion: SchemaVersion, RunID: runID, Status: "usage-only",
		Note: "Pricing, ALB LCU, Fargate duration, log bytes, public IPv4 time, ECR storage and confirmed billing are added after AWS measurement.",
	}
	var cpu uint64
	for _, task := range tasks {
		counter := task.Counters
		cpu += counter.CPUUsageNanos
		cost.DerivativeGETRequests += counter.DerivativeGetHit + counter.DerivativeGetMiss + counter.DerivativeGetError
		cost.OriginalGETRequests += counter.OriginalGetCount
		cost.ConditionalPUTRequests += counter.PublishCreated + counter.PublishExisting + counter.PublishConflict + counter.PublishError
		cost.PublishAttemptBytes += counter.PublishAttemptBytes
	}
	cost.MeasuredTaskCPUSeconds = float64(cpu) / float64(time.Second)
	return cost
}

func uploadDirectory(ctx context.Context, config Config, storage Storage, directory string) error {
	return filepath.WalkDir(directory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		key := strings.Trim(strings.TrimSpace(config.ResultPrefix), "/")
		if key != "" {
			key += "/"
		}
		key += config.RunID + "/" + filepath.ToSlash(relative)
		return storage.PutResult(ctx, config.ResultBucket, key, data)
	})
}

func validateConfig(config Config) error {
	if !validPathSegment(config.RunID) {
		return errors.New("run ID must be a non-empty path segment")
	}
	if config.ResultsRoot == "" || config.Region == "" || config.GitCommit == "" || config.ContainerDigest == "" {
		return errors.New("results root, region, git commit and container digest are required")
	}
	if err := validateHex(config.GitCommit, 40, "git commit"); err != nil {
		return err
	}
	if config.Repetitions < 1 || config.RequestTimeout <= 0 || config.ControlTimeout <= 0 || config.ControlPollGap <= 0 || config.StartSkewLimit < 0 {
		return errors.New("repetitions, timeouts and poll gap must be positive")
	}
	if !config.Calibration && config.StartSkewLimit == 0 {
		return errors.New("retained runs require a positive start skew limit fixed by calibration")
	}
	if !strings.HasPrefix(config.ContainerDigest, "sha256:") || validateSHA256(strings.TrimPrefix(config.ContainerDigest, "sha256:"), "container image") != nil {
		return errors.New("container image digest must be sha256 followed by 64 lowercase hexadecimal characters")
	}
	if err := validateSHA256(config.SourceHash, "source"); err != nil {
		return err
	}
	if err := validateSHA256(config.DerivativeKey, "derivative"); err != nil {
		return err
	}
	if config.CanonicalSpec == "" || config.DerivativeBucket == "" || config.ResultBucket == "" {
		return errors.New("canonical spec and S3 buckets are required")
	}
	if config.ExpectedWebPBytes < 0 {
		return errors.New("expected WebP bytes cannot be negative")
	}
	if parsed, err := media.ParseTransformSpec(strings.TrimSuffix(config.CanonicalSpec, ",format=webp"), media.FormatWebP); err != nil || parsed.Canonical() != config.CanonicalSpec {
		return errors.New("canonical spec must be a canonical WebP transform specification")
	} else if derivativeKey, keyErr := media.DerivativeKey(config.SourceHash, parsed); keyErr != nil || derivativeKey != config.DerivativeKey {
		return errors.New("derivative key does not match source hash and canonical spec")
	}
	if len(config.Services) != 3 {
		return errors.New("exactly three AWS S4 service targets are required")
	}
	expected := map[string]int{ScenarioTask1: 1, ScenarioTask2: 2, ScenarioTask4: 4}
	seen := make(map[string]bool)
	for _, target := range config.Services {
		endpoint, endpointErr := url.Parse(target.Endpoint)
		validEndpoint := endpointErr == nil && endpoint != nil && (endpoint.Scheme == "http" || endpoint.Scheme == "https") && endpoint.Host != "" && endpoint.User == nil && endpoint.RawQuery == "" && endpoint.Fragment == ""
		if seen[target.Scenario] || expected[target.Scenario] != target.ExpectedTasks || !validEndpoint || target.Cluster == "" || target.Service == "" || target.TargetGroupARN == "" || target.ListenerPort < 1 {
			return fmt.Errorf("invalid service target for scenario %q", target.Scenario)
		}
		seen[target.Scenario] = true
	}
	return nil
}

func validateSHA256(value, kind string) error {
	return validateHex(value, 64, kind+" SHA-256")
}

func validateHex(value string, length int, kind string) error {
	if len(value) != length {
		return fmt.Errorf("invalid %s %q", kind, value)
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return fmt.Errorf("invalid %s %q", kind, value)
		}
	}
	return nil
}

func validPathSegment(value string) bool {
	if value == "" || value == "." || value == ".." || len(value) > 96 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '-' && character != '_' && character != '.' {
			return false
		}
	}
	return true
}
