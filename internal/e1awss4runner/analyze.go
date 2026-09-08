package e1awss4runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/parantail/content-serving-lab/internal/e1awss4"
	"github.com/parantail/content-serving-lab/internal/media"
)

type analysisDocument struct {
	SchemaVersion  string   `json:"schema_version"`
	RunID          string   `json:"run_id"`
	RawValidation  string   `json:"raw_validation"`
	Calibration    bool     `json:"calibration"`
	ValidTrials    int      `json:"valid_trials"`
	InvalidTrials  int      `json:"invalid_trials"`
	IncludedTrials []string `json:"included_trials"`
	ExcludedTrials []string `json:"excluded_trials"`
	Files          []string `json:"files"`
}

func Analyze(runDirectory string) (AnalysisOutput, error) {
	var metadata RunMetadata
	if err := readJSON(filepath.Join(runDirectory, "run.json"), &metadata); err != nil {
		return AnalysisOutput{}, err
	}
	if metadata.SchemaVersion != SchemaVersion || metadata.RunID == "" || !metadata.SanitizationApplied {
		return AnalysisOutput{}, errors.New("run metadata schema or sanitization marker is invalid")
	}
	var infrastructure Infrastructure
	if err := readJSON(filepath.Join(runDirectory, "infrastructure.json"), &infrastructure); err != nil {
		return AnalysisOutput{}, err
	}
	var cost CostDocument
	if err := readJSON(filepath.Join(runDirectory, "cost.json"), &cost); err != nil {
		return AnalysisOutput{}, err
	}
	trials, err := readTrials(filepath.Join(runDirectory, "trials.csv"))
	if err != nil {
		return AnalysisOutput{}, err
	}
	requests, err := readRequests(filepath.Join(runDirectory, "requests.csv"))
	if err != nil {
		return AnalysisOutput{}, err
	}
	tasks, err := readTasks(filepath.Join(runDirectory, "tasks.csv"))
	if err != nil {
		return AnalysisOutput{}, err
	}
	resources, err := readResources(filepath.Join(runDirectory, "resources.csv"))
	if err != nil {
		return AnalysisOutput{}, err
	}
	storage, err := readStorage(filepath.Join(runDirectory, "storage.csv"))
	if err != nil {
		return AnalysisOutput{}, err
	}
	events, err := readEvents(filepath.Join(runDirectory, "events.csv"))
	if err != nil {
		return AnalysisOutput{}, err
	}
	if err := validateRaw(metadata, infrastructure, cost, trials, requests, tasks, resources, storage, events); err != nil {
		return AnalysisOutput{}, fmt.Errorf("raw validation: %w", err)
	}

	analysisDirectory := filepath.Join(runDirectory, "analysis")
	if err := os.MkdirAll(analysisDirectory, 0o755); err != nil {
		return AnalysisOutput{}, err
	}
	if err := writeSummary(filepath.Join(analysisDirectory, "summary.csv"), trials); err != nil {
		return AnalysisOutput{}, err
	}
	if err := writeAtomic(filepath.Join(analysisDirectory, "task-distribution.svg"), []byte(taskDistributionSVG(trials, tasks))); err != nil {
		return AnalysisOutput{}, err
	}
	if err := writeAtomic(filepath.Join(analysisDirectory, "duplicate-work.svg"), []byte(duplicateWorkSVG(trials))); err != nil {
		return AnalysisOutput{}, err
	}
	if err := writeAtomic(filepath.Join(analysisDirectory, "latency-cost.svg"), []byte(latencyCostSVG(trials))); err != nil {
		return AnalysisOutput{}, err
	}

	document := analysisDocument{
		SchemaVersion: SchemaVersion, RunID: metadata.RunID, RawValidation: "passed", Calibration: metadata.Calibration,
		Files: []string{"summary.csv", "task-distribution.svg", "duplicate-work.svg", "latency-cost.svg"},
	}
	for _, trial := range trials {
		if trial.Valid {
			document.ValidTrials++
			document.IncludedTrials = append(document.IncludedTrials, trial.TrialID)
		} else {
			document.InvalidTrials++
			document.ExcludedTrials = append(document.ExcludedTrials, trial.TrialID)
		}
	}
	if err := writeJSON(filepath.Join(analysisDirectory, "analysis.json"), document); err != nil {
		return AnalysisOutput{}, err
	}
	return AnalysisOutput{Directory: analysisDirectory, RunID: metadata.RunID, ValidTrials: document.ValidTrials, InvalidTrials: document.InvalidTrials}, nil
}

func validateRaw(metadata RunMetadata, infrastructure Infrastructure, cost CostDocument, trials []TrialRecord, requests []RequestRecord, tasks []TaskRecord, resources []ResourceRecord, storage []StorageRecord, events []EventRecord) error {
	if infrastructure.SchemaVersion != SchemaVersion || infrastructure.RunID != metadata.RunID || infrastructure.Region != metadata.Region {
		return errors.New("infrastructure metadata does not match run metadata")
	}
	if cost.SchemaVersion != SchemaVersion || cost.RunID != metadata.RunID {
		return errors.New("cost metadata does not match run metadata")
	}
	if metadata.RequestsPerTrial != RequestsPerTrial || metadata.Repetitions < 1 || len(trials) != metadata.Repetitions*3 || len(metadata.ExecutionOrder) != len(trials) {
		return errors.New("trial count or requests-per-trial contract is invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, metadata.CreatedAt); err != nil || metadata.RequestTimeoutMS <= 0 || metadata.ControlTimeoutMS <= 0 || metadata.ControlPollGapMS <= 0 || metadata.StartSkewLimitMS < 0 || (!metadata.Calibration && metadata.StartSkewLimitMS == 0) {
		return errors.New("run timing metadata is invalid")
	}
	if !strings.HasPrefix(metadata.ContainerDigest, "sha256:") || validateSHA256(strings.TrimPrefix(metadata.ContainerDigest, "sha256:"), "container image") != nil || strings.Contains(metadata.ContainerDigest, "arn:aws") || strings.Contains(metadata.ContainerDigest, "://") {
		return errors.New("run metadata contains an unsanitized identifier")
	}
	if err := validateSHA256(metadata.SourceHash, "source"); err != nil {
		return err
	}
	if err := validateSHA256(metadata.DerivativeKey, "derivative"); err != nil {
		return err
	}
	if err := validateHex(metadata.GitCommit, 40, "git commit"); err != nil {
		return err
	}
	spec, err := media.ParseTransformSpec(strings.TrimSuffix(metadata.CanonicalSpec, ",format=webp"), media.FormatWebP)
	if err != nil || spec.Canonical() != metadata.CanonicalSpec {
		return errors.New("run canonical transform specification is invalid")
	}
	derived, err := media.DerivativeKey(metadata.SourceHash, spec)
	if err != nil || derived != metadata.DerivativeKey {
		return errors.New("run derivative key does not match its source and transform")
	}
	if len(infrastructure.Services) != 3 {
		return errors.New("infrastructure must contain three service summaries")
	}

	requestsByTrial := groupRequests(requests)
	tasksByTrial := groupTasks(tasks)
	resourcesByTrialTask := groupResources(resources)
	storageByTrial := groupStorage(storage)
	eventsByTrial := groupEvents(events)
	declaredByID := make(map[string]TrialRecord, len(trials))
	recomputed := make([]TrialRecord, 0, len(trials))
	actualOrder := make([]string, 0, len(trials))
	for _, trial := range trials {
		if trial.RunID != metadata.RunID || declaredByID[trial.TrialID].TrialID != "" {
			return fmt.Errorf("duplicate or mismatched trial %q", trial.TrialID)
		}
		declaredByID[trial.TrialID] = trial
		actualOrder = append(actualOrder, trial.Scenario)
	}
	if err := validateRecordOwnership(metadata.RunID, declaredByID, requests, tasks, resources, storage, events); err != nil {
		return err
	}
	for _, trial := range trials {
		target := ServiceTarget{Scenario: trial.Scenario, Repetition: trial.Repetition, ExpectedTasks: expectedTasks(trial.Scenario)}
		if target.ExpectedTasks == 0 || trial.ExpectedTasks != target.ExpectedTasks || trial.TrialID != trialID(metadata.RunID, target) {
			return fmt.Errorf("trial %s scenario or repetition is invalid", trial.TrialID)
		}
		trialRequests := requestsByTrial[trial.TrialID]
		trialTasks := tasksByTrial[trial.TrialID]
		if len(trialRequests) != RequestsPerTrial {
			return fmt.Errorf("trial %s request rows = %d, want %d", trial.TrialID, len(trialRequests), RequestsPerTrial)
		}
		if len(trialTasks) != target.ExpectedTasks {
			return fmt.Errorf("trial %s task rows = %d, want %d", trial.TrialID, len(trialTasks), target.ExpectedTasks)
		}
		if err := validateTaskAndRequestRows(metadata, trial, trialRequests, trialTasks); err != nil {
			return err
		}
		if err := validateResourceRows(trial, trialTasks, resourcesByTrialTask); err != nil {
			return err
		}
		if err := validateStorageRows(metadata.RunID, trial, trialTasks, storageByTrial[trial.TrialID]); err != nil {
			return err
		}
		if err := validateEventRows(metadata.RunID, trial, eventsByTrial[trial.TrialID]); err != nil {
			return err
		}
		config := Config{RunID: metadata.RunID, DerivativeKey: metadata.DerivativeKey, StartSkewLimit: time.Duration(metadata.StartSkewLimitMS * float64(time.Millisecond)), ExpectedWebPBytes: metadata.ExpectedWebPBytes}
		computed := summarizeTrial(
			config, target,
			HealthSnapshot{Stable: trial.DeploymentStableBefore, HealthyTaskIDs: trial.HealthyBeforeTaskIDs},
			HealthSnapshot{Stable: trial.DeploymentStableAfter, HealthyTaskIDs: trial.HealthyAfterTaskIDs},
			trial.PreparedTaskIDs, trial.FinishedTaskIDs, trial.ColdConfirmed, trial.StoredSHA256, trial.StoredBytes,
			trial.StoredWebPValid, trialRequests, trialTasks,
		)
		recomputed = append(recomputed, computed)
	}
	enforceRunTaskSets(recomputed)
	for _, computed := range recomputed {
		if err := compareTrials(declaredByID[computed.TrialID], computed); err != nil {
			return err
		}
	}
	if !reflect.DeepEqual(actualOrder, metadata.ExecutionOrder) {
		return errors.New("run execution order does not match trials")
	}
	if err := validateSchedule(metadata, trials); err != nil {
		return err
	}
	if err := validateInfrastructure(infrastructure, tasks, metadata.Region, metadata.ContainerDigest); err != nil {
		return err
	}
	wantCost := buildCost(metadata.RunID, tasks)
	if cost.Status != "usage-only" || !reflect.DeepEqual(cost, wantCost) {
		return errors.New("cost usage counters do not match task rows")
	}
	return nil
}

func validateRecordOwnership(runID string, trials map[string]TrialRecord, requests []RequestRecord, tasks []TaskRecord, resources []ResourceRecord, storage []StorageRecord, events []EventRecord) error {
	taskIDs := make(map[string]map[string]bool)
	for _, task := range tasks {
		trial, exists := trials[task.TrialID]
		if !exists || task.RunID != runID || task.Scenario != trial.Scenario {
			return fmt.Errorf("task row refers to unknown or mismatched trial %q", task.TrialID)
		}
		if taskIDs[task.TrialID] == nil {
			taskIDs[task.TrialID] = make(map[string]bool)
		}
		taskIDs[task.TrialID][task.Task.TaskID] = true
	}
	for _, request := range requests {
		trial, exists := trials[request.TrialID]
		if !exists || request.RunID != runID || request.Scenario != trial.Scenario {
			return fmt.Errorf("request row refers to unknown or mismatched trial %q", request.TrialID)
		}
	}
	for _, resource := range resources {
		trial, exists := trials[resource.TrialID]
		if !exists || resource.RunID != runID || resource.Scenario != trial.Scenario || !taskIDs[resource.TrialID][resource.TaskID] {
			return fmt.Errorf("resource row refers to unknown task or trial %q", resource.TrialID)
		}
	}
	for _, row := range storage {
		trial, exists := trials[row.TrialID]
		if !exists || row.RunID != runID || row.Scenario != trial.Scenario {
			return fmt.Errorf("storage row refers to unknown or mismatched trial %q", row.TrialID)
		}
	}
	for _, event := range events {
		trial, exists := trials[event.TrialID]
		if !exists || event.RunID != runID || event.Scenario != trial.Scenario {
			return fmt.Errorf("event row refers to unknown or mismatched trial %q", event.TrialID)
		}
	}
	return nil
}

func validateTaskAndRequestRows(metadata RunMetadata, trial TrialRecord, requests []RequestRecord, tasks []TaskRecord) error {
	taskByID := make(map[string]TaskRecord, len(tasks))
	requestIDs := make(map[string]bool, len(requests))
	for _, task := range tasks {
		if task.RunID != metadata.RunID || task.TrialID != trial.TrialID || task.Scenario != trial.Scenario || task.Task.TaskID == "" || taskByID[task.Task.TaskID].Task.TaskID != "" {
			return fmt.Errorf("trial %s has duplicate or mismatched task rows", trial.TrialID)
		}
		if strings.Contains(task.Task.TaskID, "/") || strings.Contains(task.Task.TaskID, ":") || strings.Contains(task.Task.TaskID, "arn:aws") {
			return fmt.Errorf("trial %s has unsanitized task ID", trial.TrialID)
		}
		if task.Counters.RequestsInflight != 0 || task.Counters.TransformInflight != 0 || task.Counters.CoordinatorKeys != 0 || task.Counters.CoordinatorWaiters != 0 {
			return fmt.Errorf("trial %s task %s is not drained", trial.TrialID, task.Task.TaskID)
		}
		if err := validateTaskCounters(trial, task); err != nil {
			return err
		}
		prepared, prepareErr := time.Parse(time.RFC3339Nano, task.PreparedAt)
		finished, finishErr := time.Parse(time.RFC3339Nano, task.FinishedAt)
		first, firstErr := time.Parse(time.RFC3339Nano, task.FirstRequestAt)
		last, lastErr := time.Parse(time.RFC3339Nano, task.LastRequestAt)
		if prepareErr != nil || finishErr != nil || firstErr != nil || lastErr != nil || prepared.After(first) || first.After(last) || last.After(finished) {
			return fmt.Errorf("trial %s task %s timestamps are invalid", trial.TrialID, task.Task.TaskID)
		}
		taskByID[task.Task.TaskID] = task
	}
	counts := make(map[string]int64)
	cacheHits := make(map[string]int64)
	failed := make(map[string]bool)
	for _, request := range requests {
		if request.RunID != metadata.RunID || request.TrialID != trial.TrialID || request.Scenario != trial.Scenario || request.RequestID == "" || requestIDs[request.RequestID] {
			return fmt.Errorf("trial %s has duplicate or mismatched request rows", trial.TrialID)
		}
		requestIDs[request.RequestID] = true
		task, exists := taskByID[request.TaskID]
		if !exists || request.AvailabilityZone != task.Task.AvailabilityZone {
			return fmt.Errorf("trial %s request %s task or AZ does not match task rows", trial.TrialID, request.RequestID)
		}
		started, startErr := time.Parse(time.RFC3339Nano, request.StartedAt)
		finished, finishErr := time.Parse(time.RFC3339Nano, request.FinishedAt)
		if startErr != nil || finishErr != nil || finished.Before(started) || math.Abs(milliseconds(finished.Sub(started))-request.LatencyMS) > 0.001 {
			return fmt.Errorf("trial %s request %s timing is invalid", trial.TrialID, request.RequestID)
		}
		if request.Cache != "miss" && request.Cache != "derivative" {
			return fmt.Errorf("trial %s request %s cache result is invalid", trial.TrialID, request.RequestID)
		}
		counts[request.TaskID]++
		if request.Cache == "derivative" {
			cacheHits[request.TaskID]++
		}
		if request.HTTPStatus != http.StatusOK || request.ErrorType != "" {
			failed[request.TaskID] = true
		}
	}
	for taskID, task := range taskByID {
		if counts[taskID] != task.Counters.ImageRequests {
			return fmt.Errorf("trial %s task %s request counter mismatch", trial.TrialID, taskID)
		}
		if !failed[taskID] {
			c := task.Counters
			// Initial misses enter coordination. Each leader rechecks S3; a
			// recheck miss reads the original, and a lost publish reads the winner.
			misses := counts[taskID] - cacheHits[taskID]
			leaders := misses - c.CoalescedRequests
			wantMiss := misses + c.OriginalGetCount
			wantHit := cacheHits[taskID] + leaders - c.OriginalGetCount + c.PublishExisting
			if leaders < c.OriginalGetCount || c.DerivativeGetError != 0 || c.DerivativeGetMiss != wantMiss || c.DerivativeGetHit != wantHit {
				return fmt.Errorf("trial %s task %s derivative GET equations do not balance: hit=%d want=%d miss=%d want=%d", trial.TrialID, taskID, c.DerivativeGetHit, wantHit, c.DerivativeGetMiss, wantMiss)
			}
		}
	}
	return nil
}

func validateTaskCounters(trial TrialRecord, task TaskRecord) error {
	counter := task.Counters
	values := []int64{
		counter.ImageRequests, counter.RequestsInflight, counter.DerivativeGetHit, counter.DerivativeGetMiss,
		counter.DerivativeGetError, counter.DerivativeGetBytes, counter.OriginalGetCount, counter.OriginalGetSuccess,
		counter.OriginalGetError, counter.OriginalGetBytes, counter.TransformAttempts, counter.TransformSuccess,
		counter.TransformFailure, counter.TransformError, counter.TransformTimeout, counter.TransformDurationNanos,
		counter.TransformInflight, counter.TransformMaxInflight, counter.CoalescedRequests, counter.PublishCreated,
		counter.PublishExisting, counter.PublishConflict, counter.PublishError, counter.PublishAttemptBytes, counter.ResourceErrors,
	}
	for _, value := range values {
		if value < 0 {
			return fmt.Errorf("trial %s task %s has a negative counter", trial.TrialID, task.Task.TaskID)
		}
	}
	if counter.CoordinatorKeys < 0 || counter.CoordinatorWaiters < 0 ||
		counter.OriginalGetCount != counter.OriginalGetSuccess+counter.OriginalGetError ||
		counter.TransformAttempts != counter.TransformSuccess+counter.TransformFailure ||
		counter.TransformFailure != counter.TransformError+counter.TransformTimeout ||
		counter.PublishCreated+counter.PublishExisting+counter.PublishError != counter.TransformSuccess {
		return fmt.Errorf("trial %s task %s counter equations do not balance", trial.TrialID, task.Task.TaskID)
	}
	return nil
}

func validateResourceRows(trial TrialRecord, tasks []TaskRecord, grouped map[string][]ResourceRecord) error {
	for _, task := range tasks {
		key := trial.TrialID + "\x00" + task.Task.TaskID
		rows := grouped[key]
		if len(rows) < 2 {
			return fmt.Errorf("trial %s task %s resource samples = %d, want at least 2", trial.TrialID, task.Task.TaskID, len(rows))
		}
		var firstCPU, lastCPU uint64
		var haveCPU bool
		var peak uint64
		var sampleErrors int64
		var previous time.Time
		for _, row := range rows {
			if row.RunID != trial.RunID || row.TrialID != trial.TrialID || row.Scenario != trial.Scenario || row.TaskID != task.Task.TaskID {
				return fmt.Errorf("trial %s has mismatched resource rows", trial.TrialID)
			}
			timestamp, err := time.Parse(time.RFC3339Nano, row.Timestamp)
			if err != nil || (!previous.IsZero() && timestamp.Before(previous)) {
				return fmt.Errorf("trial %s task %s resource timestamps are invalid", trial.TrialID, task.Task.TaskID)
			}
			previous = timestamp
			if row.Error != "" {
				sampleErrors++
				continue
			}
			if !haveCPU {
				firstCPU, haveCPU = row.CPUUsageNanos, true
			}
			lastCPU = row.CPUUsageNanos
			peak = max(peak, row.MemoryUsageBytes)
		}
		var cpuDelta uint64
		if haveCPU && lastCPU >= firstCPU {
			cpuDelta = lastCPU - firstCPU
		}
		if cpuDelta != task.Counters.CPUUsageNanos || peak != task.Counters.PeakMemoryBytes || sampleErrors != task.Counters.ResourceErrors {
			return fmt.Errorf("trial %s task %s resource counters do not match samples", trial.TrialID, task.Task.TaskID)
		}
	}
	return nil
}

func validateStorageRows(runID string, trial TrialRecord, tasks []TaskRecord, rows []StorageRecord) error {
	reports := make(map[string]e1awss4.TrialReport, len(tasks))
	for _, task := range tasks {
		reports[task.Task.TaskID] = e1awss4.TrialReport{Task: task.Task, Counters: task.Counters}
	}
	_, _, expectedRows := flattenTaskReports(Config{RunID: runID}, ServiceTarget{Scenario: trial.Scenario}, trial.TrialID, reports)
	expectedRows = append(expectedRows,
		StorageRecord{RunID: runID, TrialID: trial.TrialID, Scenario: trial.Scenario, Actor: "load-generator", Operation: "delete_object", Result: "success", Requests: 1},
		StorageRecord{RunID: runID, TrialID: trial.TrialID, Scenario: trial.Scenario, Actor: "load-generator", Operation: "head_object", Result: map[bool]string{true: "missing", false: "found"}[trial.ColdConfirmed], Requests: 1},
		StorageRecord{RunID: runID, TrialID: trial.TrialID, Scenario: trial.Scenario, Actor: "load-generator", Operation: "get_object", Result: "success", Requests: 1, Bytes: trial.StoredBytes},
	)
	if !reflect.DeepEqual(sortedStorage(rows), sortedStorage(expectedRows)) {
		return fmt.Errorf("trial %s storage rows do not match task counters", trial.TrialID)
	}
	return nil
}

func validateEventRows(runID string, trial TrialRecord, rows []EventRecord) error {
	want := []string{"trial_started", "health_before", "tasks_prepared", "derivative_deleted", "cold_checked", "burst_ready", "burst_released", "burst_finished", "tasks_finished", "derivative_verified", "health_after"}
	if len(rows) != len(want) {
		return fmt.Errorf("trial %s event rows = %d, want %d", trial.TrialID, len(rows), len(want))
	}
	var previous time.Time
	for index, row := range rows {
		if row.RunID != runID || row.TrialID != trial.TrialID || row.Scenario != trial.Scenario || row.Name != want[index] {
			return fmt.Errorf("trial %s event sequence mismatch at %d", trial.TrialID, index)
		}
		timestamp, err := time.Parse(time.RFC3339Nano, row.Timestamp)
		if err != nil || (!previous.IsZero() && timestamp.Before(previous)) {
			return fmt.Errorf("trial %s event timestamp is invalid", trial.TrialID)
		}
		previous = timestamp
	}
	return nil
}

func compareTrials(declared, computed TrialRecord) error {
	if !reflect.DeepEqual(declared, computed) {
		return fmt.Errorf("trial %s summary does not match request and task rows", declared.TrialID)
	}
	return nil
}

func validateSchedule(metadata RunMetadata, trials []TrialRecord) error {
	for repetition := 1; repetition <= metadata.Repetitions; repetition++ {
		order := []string{ScenarioTask1, ScenarioTask2, ScenarioTask4}
		if repetition%2 == 0 {
			order = []string{ScenarioTask4, ScenarioTask2, ScenarioTask1}
		}
		for offset, scenario := range order {
			trial := trials[(repetition-1)*3+offset]
			if trial.Repetition != repetition || trial.Scenario != scenario {
				return fmt.Errorf("trial schedule mismatch at repetition %d", repetition)
			}
		}
	}
	return nil
}

func validateInfrastructure(infrastructure Infrastructure, tasks []TaskRecord, region, containerDigest string) error {
	wantServices := map[string]int{ScenarioTask1: 1, ScenarioTask2: 2, ScenarioTask4: 4}
	for _, service := range infrastructure.Services {
		if wantServices[service.Scenario] != service.ExpectedTasks || service.ListenerPort < 1 || service.TargetAlgorithm != "round_robin" || service.Stickiness {
			return errors.New("infrastructure service contract is invalid")
		}
		delete(wantServices, service.Scenario)
	}
	if len(wantServices) != 0 {
		return errors.New("infrastructure service scenarios are incomplete")
	}
	identities := make(map[string]e1awss4.TaskIdentity)
	for _, task := range tasks {
		if existing, ok := identities[task.Task.TaskID]; ok && existing != task.Task {
			return fmt.Errorf("task identity changed for %s", task.Task.TaskID)
		}
		identities[task.Task.TaskID] = task.Task
	}
	if len(identities) != len(infrastructure.Tasks) {
		return errors.New("infrastructure task identity count does not match task rows")
	}
	for _, identity := range infrastructure.Tasks {
		if identities[identity.TaskID] != identity || identity.LaunchType != "FARGATE" || identity.CPUVCpu != 1 || identity.MemoryMiB != 2048 || identity.ImageDigest != containerDigest || len(identity.ImageDigest) != 71 || !strings.HasPrefix(identity.ImageDigest, "sha256:") || identity.TaskDefinitionFamily == "" || identity.TaskDefinitionRevision == "" || !strings.HasPrefix(identity.AvailabilityZone, region) {
			return fmt.Errorf("infrastructure task identity %q is invalid", identity.TaskID)
		}
	}
	return nil
}

func readTrials(path string) ([]TrialRecord, error) {
	rows, err := readCSV(path)
	if err != nil {
		return nil, err
	}
	result := make([]TrialRecord, 0, len(rows))
	for _, row := range rows {
		p := rowParser{row: row}
		record := TrialRecord{
			RunID: p.s("run_id"), TrialID: p.s("trial_id"), Scenario: p.s("scenario"), Repetition: p.i("repetition"), ExpectedTasks: p.i("expected_tasks"),
			DeploymentStableBefore: p.b("deployment_stable_before"), DeploymentStableAfter: p.b("deployment_stable_after"),
			HealthyBeforeTaskIDs: splitIDs(p.s("healthy_before_task_ids")), HealthyAfterTaskIDs: splitIDs(p.s("healthy_after_task_ids")),
			PreparedTaskIDs: splitIDs(p.s("prepared_task_ids")), FinishedTaskIDs: splitIDs(p.s("finished_task_ids")), ColdConfirmed: p.b("cold_confirmed"),
			TotalRequests: p.i("total_requests"), SuccessRequests: p.i("success_requests"), HTTP5xx: p.i("http_5xx"), Timeouts: p.i("timeouts"),
			ConnectionErrors: p.i("connection_errors"), StartSkewMS: p.f("start_skew_ms"), ResponseSHA256: p.s("response_sha256"),
			ResponseBytes: p.i64("response_bytes"), StoredSHA256: p.s("stored_sha256"), StoredBytes: p.i64("stored_bytes"), StoredWebPValid: p.b("stored_webp_valid"),
			ImageRequests: p.i64("image_requests"), DerivativeGetHit: p.i64("derivative_get_hit"), DerivativeGetMiss: p.i64("derivative_get_miss"),
			DerivativeGetError: p.i64("derivative_get_error"), DerivativeGetBytes: p.i64("derivative_get_bytes"), OriginalGetCount: p.i64("original_get_count"),
			OriginalGetBytes: p.i64("original_get_bytes"), TransformAttempts: p.i64("transform_attempts"), TransformSuccess: p.i64("transform_success"),
			TransformFailure: p.i64("transform_failure"), TransformDurationNanos: p.i64("transform_duration_nanos"), TransformMaxInflight: p.i64("transform_max_inflight"),
			TransformInflight: p.i64("transform_inflight"), CoalescedRequests: p.i64("coalesced_requests"), PublishCreated: p.i64("publish_created"),
			PublishExisting: p.i64("publish_existing"), PublishConflict: p.i64("publish_conflict"), PublishError: p.i64("publish_error"),
			PublishAttemptBytes: p.i64("publish_attempt_bytes"), CoordinatorKeys: p.i("coordinator_keys"), CoordinatorWaiters: p.i("coordinator_waiters"),
			CPUUsageNanos: p.u64("cpu_usage_nanos"), PeakMemoryBytes: p.u64("peak_memory_bytes"), ResourceErrors: p.i64("resource_errors"),
			P50MS: p.f("p50_ms"), P95MS: p.f("p95_ms"), P99MS: p.f("p99_ms"), Valid: p.b("valid"), InvalidReason: p.s("invalid_reason"),
		}
		if p.err != nil {
			return nil, p.err
		}
		result = append(result, record)
	}
	return result, nil
}

func readRequests(path string) ([]RequestRecord, error) {
	rows, err := readCSV(path)
	if err != nil {
		return nil, err
	}
	result := make([]RequestRecord, 0, len(rows))
	for _, row := range rows {
		p := rowParser{row: row}
		record := RequestRecord{
			RunID: p.s("run_id"), TrialID: p.s("trial_id"), Scenario: p.s("scenario"), RequestID: p.s("request_id"),
			TaskID: p.s("task_id"), AvailabilityZone: p.s("availability_zone"), StartedAt: p.s("started_at"), FinishedAt: p.s("finished_at"),
			LatencyMS: p.f("latency_ms"), HTTPStatus: p.i("http_status"), ResponseBytes: p.i64("response_bytes"), ResponseSHA256: p.s("response_sha256"),
			DerivativeKey: p.s("derivative_key"), Cache: p.s("cache"), Coalesced: p.b("coalesced"), TrialHeader: p.s("trial_header"), ErrorType: p.s("error_type"),
		}
		if p.err != nil {
			return nil, p.err
		}
		result = append(result, record)
	}
	return result, nil
}

func readTasks(path string) ([]TaskRecord, error) {
	rows, err := readCSV(path)
	if err != nil {
		return nil, err
	}
	result := make([]TaskRecord, 0, len(rows))
	for _, row := range rows {
		p := rowParser{row: row}
		record := TaskRecord{
			RunID: p.s("run_id"), TrialID: p.s("trial_id"), Scenario: p.s("scenario"),
			Task: e1awss4.TaskIdentity{
				TaskID: p.s("task_id"), TaskDefinitionFamily: p.s("task_definition_family"), TaskDefinitionRevision: p.s("task_definition_revision"),
				AvailabilityZone: p.s("availability_zone"), LaunchType: p.s("launch_type"), CPUVCpu: p.f("cpu_vcpu"),
				MemoryMiB: p.i64("memory_mib"), ImageDigest: p.s("image_digest"),
			},
			PreparedAt: p.s("prepared_at"), FinishedAt: p.s("finished_at"), FirstRequestAt: p.s("first_request_at"), LastRequestAt: p.s("last_request_at"),
			Counters: e1awss4.TrialCounters{
				ImageRequests: p.i64("image_requests"), RequestsInflight: p.i64("requests_inflight"), DerivativeGetHit: p.i64("derivative_get_hit"),
				DerivativeGetMiss: p.i64("derivative_get_miss"), DerivativeGetError: p.i64("derivative_get_error"), DerivativeGetBytes: p.i64("derivative_get_bytes"),
				OriginalGetCount: p.i64("original_get_count"), OriginalGetSuccess: p.i64("original_get_success"), OriginalGetError: p.i64("original_get_error"),
				OriginalGetBytes: p.i64("original_get_bytes"), TransformAttempts: p.i64("transform_attempts"), TransformSuccess: p.i64("transform_success"),
				TransformFailure: p.i64("transform_failure"), TransformError: p.i64("transform_error"), TransformTimeout: p.i64("transform_timeout"),
				TransformDurationNanos: p.i64("transform_duration_nanos"), TransformInflight: p.i64("transform_inflight"), TransformMaxInflight: p.i64("transform_max_inflight"),
				CoalescedRequests: p.i64("coalesced_requests"), PublishCreated: p.i64("publish_created"), PublishExisting: p.i64("publish_existing"),
				PublishConflict: p.i64("publish_conflict"), PublishError: p.i64("publish_error"), PublishAttemptBytes: p.i64("publish_attempt_bytes"),
				CoordinatorKeys: p.i("coordinator_keys"), CoordinatorWaiters: p.i("coordinator_waiters"), CPUUsageNanos: p.u64("cpu_usage_nanos"),
				PeakMemoryBytes: p.u64("peak_memory_bytes"), ResourceErrors: p.i64("resource_errors"),
			},
		}
		if p.err != nil {
			return nil, p.err
		}
		result = append(result, record)
	}
	return result, nil
}

func readResources(path string) ([]ResourceRecord, error) {
	rows, err := readCSV(path)
	if err != nil {
		return nil, err
	}
	result := make([]ResourceRecord, 0, len(rows))
	for _, row := range rows {
		p := rowParser{row: row}
		record := ResourceRecord{RunID: p.s("run_id"), TrialID: p.s("trial_id"), Scenario: p.s("scenario"), TaskID: p.s("task_id"), Timestamp: p.s("timestamp"), CPUUsageNanos: p.u64("cpu_usage_nanos"), MemoryUsageBytes: p.u64("memory_usage_bytes"), Error: p.s("error")}
		if p.err != nil {
			return nil, p.err
		}
		result = append(result, record)
	}
	return result, nil
}

func readStorage(path string) ([]StorageRecord, error) {
	rows, err := readCSV(path)
	if err != nil {
		return nil, err
	}
	result := make([]StorageRecord, 0, len(rows))
	for _, row := range rows {
		p := rowParser{row: row}
		record := StorageRecord{RunID: p.s("run_id"), TrialID: p.s("trial_id"), Scenario: p.s("scenario"), Actor: p.s("actor"), TaskID: p.s("task_id"), Operation: p.s("operation"), Result: p.s("result"), Requests: p.i64("requests"), Bytes: p.i64("bytes")}
		if p.err != nil {
			return nil, p.err
		}
		result = append(result, record)
	}
	return result, nil
}

func readEvents(path string) ([]EventRecord, error) {
	rows, err := readCSV(path)
	if err != nil {
		return nil, err
	}
	result := make([]EventRecord, 0, len(rows))
	for _, row := range rows {
		p := rowParser{row: row}
		record := EventRecord{RunID: p.s("run_id"), TrialID: p.s("trial_id"), Scenario: p.s("scenario"), Name: p.s("name"), Timestamp: p.s("timestamp"), Detail: p.s("detail")}
		if p.err != nil {
			return nil, p.err
		}
		result = append(result, record)
	}
	return result, nil
}

type rowParser struct {
	row map[string]string
	err error
}

func (p *rowParser) s(name string) string {
	if p.err != nil {
		return ""
	}
	value, ok := p.row[name]
	if !ok {
		p.err = fmt.Errorf("CSV is missing column %q", name)
	}
	return value
}

func (p *rowParser) i(name string) int {
	value, err := strconv.Atoi(p.s(name))
	if err != nil && p.err == nil {
		p.err = fmt.Errorf("column %s: %w", name, err)
	}
	return value
}

func (p *rowParser) i64(name string) int64 {
	value, err := strconv.ParseInt(p.s(name), 10, 64)
	if err != nil && p.err == nil {
		p.err = fmt.Errorf("column %s: %w", name, err)
	}
	return value
}

func (p *rowParser) u64(name string) uint64 {
	value, err := strconv.ParseUint(p.s(name), 10, 64)
	if err != nil && p.err == nil {
		p.err = fmt.Errorf("column %s: %w", name, err)
	}
	return value
}

func (p *rowParser) f(name string) float64 {
	value, err := strconv.ParseFloat(p.s(name), 64)
	if (err != nil || math.IsNaN(value) || math.IsInf(value, 0)) && p.err == nil {
		p.err = fmt.Errorf("column %s is not a finite number", name)
	}
	return value
}

func (p *rowParser) b(name string) bool {
	value, err := strconv.ParseBool(p.s(name))
	if err != nil && p.err == nil {
		p.err = fmt.Errorf("column %s: %w", name, err)
	}
	return value
}

func groupRequests(records []RequestRecord) map[string][]RequestRecord {
	result := make(map[string][]RequestRecord)
	for _, record := range records {
		result[record.TrialID] = append(result[record.TrialID], record)
	}
	return result
}

func groupTasks(records []TaskRecord) map[string][]TaskRecord {
	result := make(map[string][]TaskRecord)
	for _, record := range records {
		result[record.TrialID] = append(result[record.TrialID], record)
	}
	return result
}

func groupResources(records []ResourceRecord) map[string][]ResourceRecord {
	result := make(map[string][]ResourceRecord)
	for _, record := range records {
		key := record.TrialID + "\x00" + record.TaskID
		result[key] = append(result[key], record)
	}
	return result
}

func groupStorage(records []StorageRecord) map[string][]StorageRecord {
	result := make(map[string][]StorageRecord)
	for _, record := range records {
		result[record.TrialID] = append(result[record.TrialID], record)
	}
	return result
}

func groupEvents(records []EventRecord) map[string][]EventRecord {
	result := make(map[string][]EventRecord)
	for _, record := range records {
		result[record.TrialID] = append(result[record.TrialID], record)
	}
	return result
}

func sortedStorage(records []StorageRecord) []StorageRecord {
	result := append([]StorageRecord(nil), records...)
	sort.Slice(result, func(i, j int) bool {
		left := result[i].Actor + "\x00" + result[i].TaskID + "\x00" + result[i].Operation + "\x00" + result[i].Result
		right := result[j].Actor + "\x00" + result[j].TaskID + "\x00" + result[j].Operation + "\x00" + result[j].Result
		return left < right
	})
	return result
}

func expectedTasks(scenario string) int {
	switch scenario {
	case ScenarioTask1:
		return 1
	case ScenarioTask2:
		return 2
	case ScenarioTask4:
		return 4
	default:
		return 0
	}
}

func readJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	return nil
}
