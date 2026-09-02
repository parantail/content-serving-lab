package e1phaseb

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type AnalysisOutput struct {
	Directory     string
	RunID         string
	ValidTrials   int
	InvalidTrials int
}

type analysisDocument struct {
	SchemaVersion     string   `json:"schema_version"`
	RunID             string   `json:"run_id"`
	RawValidation     string   `json:"raw_validation"`
	ValidTrials       int      `json:"valid_trials"`
	InvalidTrials     int      `json:"invalid_trials"`
	ExpectedScenarios []string `json:"expected_scenarios"`
	Files             []string `json:"files"`
}

type analysisTrial struct {
	ID                   string
	Scenario             string
	Valid                bool
	Concurrency          int
	ProcessCount         int
	TotalRequests        int
	SuccessRequests      int
	ErrorRequests        int
	CanceledRequests     int
	DerivativeHits       int64
	DerivativeMisses     int64
	TransformAttempts    int64
	OriginalReads        int64
	PublishCreated       int64
	PublishExisting      int64
	PublishErrors        int64
	CoalescedRequests    int64
	TransformMaxInflight int64
	TransformInflight    int64
	ActiveTasks          int
	DerivativeFiles      int
	StartSkewMS          float64
}

type analysisRequest struct {
	TrialID        string
	Scenario       string
	RequestID      string
	Class          string
	Role           string
	TargetTaskID   string
	TaskID         string
	HTTPStatus     int
	ErrorType      string
	LatencyMS      float64
	ResponseSHA256 string
	Coalesced      bool
	Cache          string
}

type summaryRow struct {
	Scenario              string
	RequestClass          string
	ValidTrials           int
	Requests              int
	P50MeanMS             float64
	P95MeanMS             float64
	P99MeanMS             float64
	TransformAttemptsMean float64
	PublishCreatedMean    float64
}

func Analyze(runDirectory string) (AnalysisOutput, error) {
	metadataData, err := os.ReadFile(filepath.Join(runDirectory, "run.json"))
	if err != nil {
		return AnalysisOutput{}, err
	}
	var metadata RunMetadata
	if err := json.Unmarshal(metadataData, &metadata); err != nil {
		return AnalysisOutput{}, err
	}
	if metadata.SchemaVersion != SchemaVersion {
		return AnalysisOutput{}, fmt.Errorf("schema version = %q, want %q", metadata.SchemaVersion, SchemaVersion)
	}

	trials, err := readAnalysisTrials(filepath.Join(runDirectory, "trials.csv"))
	if err != nil {
		return AnalysisOutput{}, err
	}
	requests, err := readAnalysisRequests(filepath.Join(runDirectory, "requests.csv"))
	if err != nil {
		return AnalysisOutput{}, err
	}
	metrics, err := readCSV(filepath.Join(runDirectory, "metrics.csv"))
	if err != nil {
		return AnalysisOutput{}, err
	}
	events, err := readCSV(filepath.Join(runDirectory, "events.csv"))
	if err != nil {
		return AnalysisOutput{}, err
	}
	if err := validateRaw(metadata, trials, requests, metrics, events); err != nil {
		return AnalysisOutput{}, fmt.Errorf("raw validation: %w", err)
	}

	summary := buildSummary(trials, requests)
	analysisDirectory := filepath.Join(runDirectory, "analysis")
	if err := os.MkdirAll(analysisDirectory, 0o755); err != nil {
		return AnalysisOutput{}, err
	}
	if err := writeSummary(filepath.Join(analysisDirectory, "summary.csv"), summary); err != nil {
		return AnalysisOutput{}, err
	}
	if err := writeAtomic(filepath.Join(analysisDirectory, "unrelated-latency.svg"), []byte(unrelatedLatencySVG(summary))); err != nil {
		return AnalysisOutput{}, err
	}
	if err := writeAtomic(filepath.Join(analysisDirectory, "multiprocess-work.svg"), []byte(multiprocessWorkSVG(summary))); err != nil {
		return AnalysisOutput{}, err
	}
	if err := writeAtomic(filepath.Join(analysisDirectory, "cancellation-timeline.svg"), []byte(cancellationTimelineSVG(events))); err != nil {
		return AnalysisOutput{}, err
	}

	valid, invalid := 0, 0
	for _, trial := range trials {
		if trial.Valid {
			valid++
		} else {
			invalid++
		}
	}
	document := analysisDocument{
		SchemaVersion: SchemaVersion, RunID: metadata.RunID, RawValidation: "passed",
		ValidTrials: valid, InvalidTrials: invalid,
		ExpectedScenarios: expectedScenarios(),
		Files:             []string{"summary.csv", "unrelated-latency.svg", "cancellation-timeline.svg", "multiprocess-work.svg"},
	}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return AnalysisOutput{}, err
	}
	if err := writeAtomic(filepath.Join(analysisDirectory, "analysis.json"), append(data, '\n')); err != nil {
		return AnalysisOutput{}, err
	}
	return AnalysisOutput{Directory: analysisDirectory, RunID: metadata.RunID, ValidTrials: valid, InvalidTrials: invalid}, nil
}

func readAnalysisTrials(path string) ([]analysisTrial, error) {
	rows, err := readCSV(path)
	if err != nil {
		return nil, err
	}
	result := make([]analysisTrial, 0, len(rows))
	for _, row := range rows {
		trial := analysisTrial{ID: row["trial_id"], Scenario: row["scenario"]}
		if trial.Valid, err = strconv.ParseBool(row["valid"]); err != nil {
			return nil, err
		}
		integers := []struct {
			name string
			to   *int
		}{
			{"concurrency", &trial.Concurrency}, {"process_count", &trial.ProcessCount}, {"total_requests", &trial.TotalRequests},
			{"success_requests", &trial.SuccessRequests}, {"error_requests", &trial.ErrorRequests}, {"canceled_requests", &trial.CanceledRequests},
			{"active_tasks", &trial.ActiveTasks}, {"derivative_files", &trial.DerivativeFiles},
		}
		for _, value := range integers {
			parsed, parseErr := strconv.Atoi(row[value.name])
			if parseErr != nil {
				return nil, fmt.Errorf("trial %s %s: %w", trial.ID, value.name, parseErr)
			}
			*value.to = parsed
		}
		counters := []struct {
			name string
			to   *int64
		}{
			{"derivative_hits", &trial.DerivativeHits}, {"derivative_misses", &trial.DerivativeMisses},
			{"transform_attempts", &trial.TransformAttempts}, {"original_reads", &trial.OriginalReads},
			{"publish_created", &trial.PublishCreated}, {"publish_existing", &trial.PublishExisting},
			{"publish_errors", &trial.PublishErrors}, {"coalesced_requests", &trial.CoalescedRequests},
			{"transform_max_inflight", &trial.TransformMaxInflight}, {"transform_inflight", &trial.TransformInflight},
		}
		for _, value := range counters {
			parsed, parseErr := strconv.ParseInt(row[value.name], 10, 64)
			if parseErr != nil {
				return nil, fmt.Errorf("trial %s %s: %w", trial.ID, value.name, parseErr)
			}
			*value.to = parsed
		}
		trial.StartSkewMS, err = strconv.ParseFloat(row["start_skew_ms"], 64)
		if err != nil {
			return nil, fmt.Errorf("trial %s start_skew_ms: %w", trial.ID, err)
		}
		result = append(result, trial)
	}
	return result, nil
}

func readAnalysisRequests(path string) ([]analysisRequest, error) {
	rows, err := readCSV(path)
	if err != nil {
		return nil, err
	}
	result := make([]analysisRequest, 0, len(rows))
	for _, row := range rows {
		status, err := strconv.Atoi(row["http_status"])
		if err != nil {
			return nil, err
		}
		latency, err := strconv.ParseFloat(row["latency_ms"], 64)
		if err != nil {
			return nil, err
		}
		coalesced, err := strconv.ParseBool(row["coalesced"])
		if err != nil {
			return nil, err
		}
		result = append(result, analysisRequest{
			TrialID: row["trial_id"], Scenario: row["scenario"], RequestID: row["request_id"], Class: row["request_class"],
			Role: row["request_role"], TargetTaskID: row["target_task_id"], TaskID: row["task_id"], HTTPStatus: status,
			ErrorType: row["error_type"], LatencyMS: latency, ResponseSHA256: row["response_sha256"], Coalesced: coalesced, Cache: row["cache"],
		})
	}
	return result, nil
}

func validateRaw(metadata RunMetadata, trials []analysisTrial, requests []analysisRequest, metrics, events []map[string]string) error {
	if len(trials) != metadata.Repetitions*len(expectedScenarios()) {
		return fmt.Errorf("trial count = %d, want %d", len(trials), metadata.Repetitions*len(expectedScenarios()))
	}
	trialByID := make(map[string]analysisTrial, len(trials))
	scenarioCounts := make(map[string]int)
	for _, trial := range trials {
		if trial.ID == "" || trialByID[trial.ID].ID != "" {
			return fmt.Errorf("empty or duplicate trial ID %q", trial.ID)
		}
		trialByID[trial.ID] = trial
		scenarioCounts[trial.Scenario]++
	}
	for _, scenario := range expectedScenarios() {
		if scenarioCounts[scenario] != metadata.Repetitions {
			return fmt.Errorf("scenario %s count = %d, want %d", scenario, scenarioCounts[scenario], metadata.Repetitions)
		}
	}

	requestCounts := make(map[string]struct{ total, success, failed, canceled int })
	requestIDs := make(map[string]bool)
	classCounts := make(map[string]map[string]int)
	roleCounts := make(map[string]map[string]int)
	responseHashes := make(map[string]map[string]bool)
	for _, request := range requests {
		trial, ok := trialByID[request.TrialID]
		if !ok || request.Scenario != trial.Scenario {
			return fmt.Errorf("request %s references unknown or mismatched trial %s", request.RequestID, request.TrialID)
		}
		key := request.TrialID + "\x00" + request.RequestID
		if requestIDs[key] {
			return fmt.Errorf("duplicate request %s in %s", request.RequestID, request.TrialID)
		}
		requestIDs[key] = true
		if classCounts[request.TrialID] == nil {
			classCounts[request.TrialID] = make(map[string]int)
			roleCounts[request.TrialID] = make(map[string]int)
		}
		classCounts[request.TrialID][request.Class]++
		roleCounts[request.TrialID][request.Role]++
		counts := requestCounts[request.TrialID]
		counts.total++
		if request.ErrorType == "canceled" {
			counts.canceled++
		} else if request.HTTPStatus == httpStatusOK && request.ErrorType == "" {
			counts.success++
			hashClass := request.Class
			if trial.Scenario == ScenarioF2 && hashClass == RequestClassFollowUp {
				hashClass = RequestClassHot
			}
			hashKey := request.TrialID + "\x00" + hashClass
			if responseHashes[hashKey] == nil {
				responseHashes[hashKey] = make(map[string]bool)
			}
			responseHashes[hashKey][request.ResponseSHA256] = true
		} else {
			counts.failed++
		}
		requestCounts[request.TrialID] = counts
	}
	for _, trial := range trials {
		counts := requestCounts[trial.ID]
		if counts.total != trial.TotalRequests || counts.success != trial.SuccessRequests || counts.failed != trial.ErrorRequests || counts.canceled != trial.CanceledRequests {
			return fmt.Errorf("request totals do not match trial %s", trial.ID)
		}
		if trial.Valid {
			if metadata.StartSkewLimitMS > 0 && trial.Scenario != ScenarioF2 && trial.StartSkewMS > metadata.StartSkewLimitMS {
				return fmt.Errorf("valid trial %s exceeds start skew limit", trial.ID)
			}
			if err := validateAnalysisScenario(trial); err != nil {
				return err
			}
		}
		for hashKey, hashes := range responseHashes {
			if strings.HasPrefix(hashKey, trial.ID+"\x00") && (len(hashes) != 1 || hashes[""]) {
				return fmt.Errorf("response hashes do not match for %s", hashKey)
			}
		}
		classes := classCounts[trial.ID]
		roles := roleCounts[trial.ID]
		switch trial.Scenario {
		case ScenarioS3ColdControl, ScenarioS3WarmControl:
			if classes[RequestClassUnrelated] != 10 || len(classes) != 1 || roles[RequestRoleBurst] != 10 {
				return fmt.Errorf("control request classes do not match for %s", trial.ID)
			}
		case ScenarioS3Cold, ScenarioS3Warm:
			if classes[RequestClassHot] != 90 || classes[RequestClassUnrelated] != 10 || roles[RequestRoleBurst] != 100 {
				return fmt.Errorf("mixed request classes do not match for %s", trial.ID)
			}
		case ScenarioF2:
			if roles[RequestRoleLeader] != 1 || roles[RequestRoleWaiter] != 9 || roles[RequestRoleFollowUp] != 1 || classes[RequestClassHot] != 10 || classes[RequestClassFollowUp] != 1 {
				return fmt.Errorf("F2 request roles do not match for %s", trial.ID)
			}
		case ScenarioS42, ScenarioS44:
			if classes[RequestClassHot] != 100 || roles[RequestRoleBurst] != 100 {
				return fmt.Errorf("S4 request classes do not match for %s", trial.ID)
			}
		}
	}
	for _, request := range requests {
		trial := trialByID[request.TrialID]
		if trial.ProcessCount > 0 && (request.TargetTaskID == "" || request.TaskID != request.TargetTaskID) {
			return fmt.Errorf("S4 routing mismatch for %s/%s: target %s actual %s", request.TrialID, request.RequestID, request.TargetTaskID, request.TaskID)
		}
	}

	type metricSum struct {
		hits, misses, transforms, reads, created, existing, publishErrors, coalesced int64
		tasks                                                                        map[string]bool
	}
	metricSums := make(map[string]*metricSum)
	for _, row := range metrics {
		trial, ok := trialByID[row["trial_id"]]
		if !ok || row["scenario"] != trial.Scenario {
			return fmt.Errorf("metric references unknown or mismatched trial %s", row["trial_id"])
		}
		sum := metricSums[trial.ID]
		if sum == nil {
			sum = &metricSum{tasks: make(map[string]bool)}
			metricSums[trial.ID] = sum
		}
		if sum.tasks[row["task_id"]] {
			return fmt.Errorf("duplicate task metrics %s/%s", trial.ID, row["task_id"])
		}
		sum.tasks[row["task_id"]] = true
		values := make(map[string]int64)
		for _, name := range []string{"derivative_hits", "derivative_misses", "transform_success", "transform_error", "transform_timeout", "original_success", "original_error", "publish_created", "publish_existing", "publish_error", "coalesced"} {
			parsed, err := strconv.ParseInt(row[name], 10, 64)
			if err != nil {
				return fmt.Errorf("metric %s/%s %s: %w", trial.ID, row["task_id"], name, err)
			}
			values[name] = parsed
		}
		sum.hits += values["derivative_hits"]
		sum.misses += values["derivative_misses"]
		sum.transforms += values["transform_success"] + values["transform_error"] + values["transform_timeout"]
		sum.reads += values["original_success"] + values["original_error"]
		sum.created += values["publish_created"]
		sum.existing += values["publish_existing"]
		sum.publishErrors += values["publish_error"]
		sum.coalesced += values["coalesced"]
	}
	for _, trial := range trials {
		sum := metricSums[trial.ID]
		if sum == nil {
			return fmt.Errorf("missing metrics for %s", trial.ID)
		}
		wantTasks := 1
		if trial.ProcessCount > 0 {
			wantTasks = trial.ProcessCount
		}
		if len(sum.tasks) != wantTasks {
			return fmt.Errorf("metric tasks for %s = %d, want %d", trial.ID, len(sum.tasks), wantTasks)
		}
		if sum.hits != trial.DerivativeHits || sum.misses != trial.DerivativeMisses || sum.transforms != trial.TransformAttempts || sum.reads != trial.OriginalReads || sum.created != trial.PublishCreated || sum.existing != trial.PublishExisting || sum.publishErrors != trial.PublishErrors || sum.coalesced != trial.CoalescedRequests {
			return fmt.Errorf("metric totals do not match trial %s", trial.ID)
		}
	}

	eventNames := make(map[string]map[string]int)
	for _, row := range events {
		if _, ok := trialByID[row["trial_id"]]; !ok {
			return fmt.Errorf("event references unknown trial %s", row["trial_id"])
		}
		if eventNames[row["trial_id"]] == nil {
			eventNames[row["trial_id"]] = make(map[string]int)
		}
		eventNames[row["trial_id"]][row["name"]]++
	}
	for _, trial := range trials {
		if trial.Scenario != ScenarioF2 {
			continue
		}
		expectedOrder := []string{"leader_transform_started", "waiters_joined", "leader_cancel_sent", "leader_finished", "waiters_finished", "follow_up_finished"}
		for _, name := range expectedOrder {
			if eventNames[trial.ID][name] != 1 {
				return fmt.Errorf("F2 trial %s event %s count = %d", trial.ID, name, eventNames[trial.ID][name])
			}
		}
		position := make(map[string]time.Time)
		for _, row := range events {
			if row["trial_id"] == trial.ID {
				parsed, err := time.Parse(time.RFC3339Nano, row["timestamp"])
				if err != nil {
					return fmt.Errorf("F2 trial %s event timestamp: %w", trial.ID, err)
				}
				position[row["name"]] = parsed
			}
		}
		for index := 1; index < len(expectedOrder); index++ {
			if position[expectedOrder[index]].Before(position[expectedOrder[index-1]]) {
				return fmt.Errorf("F2 trial %s event order is invalid", trial.ID)
			}
		}
	}
	return nil
}

func validateAnalysisScenario(trial analysisTrial) error {
	if trial.TransformInflight != 0 || trial.PublishErrors != 0 {
		return fmt.Errorf("valid trial %s did not drain or has publish errors", trial.ID)
	}
	switch trial.Scenario {
	case ScenarioS3ColdControl:
		if trial.TotalRequests != 10 || trial.SuccessRequests != 10 || trial.TransformAttempts != 1 || trial.OriginalReads != 1 || trial.PublishCreated != 1 || trial.DerivativeFiles != 1 {
			return fmt.Errorf("valid trial %s violates cold control contract", trial.ID)
		}
	case ScenarioS3Cold:
		if trial.TotalRequests != 100 || trial.SuccessRequests != 100 || trial.TransformAttempts != 2 || trial.OriginalReads != 2 || trial.PublishCreated != 2 || trial.TransformMaxInflight < 2 || trial.DerivativeFiles != 2 {
			return fmt.Errorf("valid trial %s violates cold mixed contract", trial.ID)
		}
	case ScenarioS3WarmControl:
		if trial.TotalRequests != 10 || trial.SuccessRequests != 10 || trial.DerivativeHits != 10 || trial.TransformAttempts != 0 || trial.OriginalReads != 0 || trial.PublishCreated != 0 || trial.DerivativeFiles != 1 {
			return fmt.Errorf("valid trial %s violates warm control contract", trial.ID)
		}
	case ScenarioS3Warm:
		if trial.TotalRequests != 100 || trial.SuccessRequests != 100 || trial.DerivativeHits != 10 || trial.TransformAttempts != 1 || trial.OriginalReads != 1 || trial.PublishCreated != 1 || trial.DerivativeFiles != 2 {
			return fmt.Errorf("valid trial %s violates warm mixed contract", trial.ID)
		}
	case ScenarioF2:
		if trial.TotalRequests != 11 || trial.SuccessRequests != 10 || trial.CanceledRequests != 1 || trial.ErrorRequests != 0 || trial.TransformAttempts != 1 || trial.OriginalReads != 1 || trial.PublishCreated != 1 || trial.DerivativeHits != 1 || trial.DerivativeFiles != 1 || trial.CoalescedRequests != 9 {
			return fmt.Errorf("valid trial %s violates cancellation contract", trial.ID)
		}
	case ScenarioS42, ScenarioS44:
		if trial.TotalRequests != 100 || trial.SuccessRequests != 100 || trial.ActiveTasks != trial.ProcessCount || trial.TransformAttempts < 1 || trial.TransformAttempts > int64(trial.ProcessCount) || trial.PublishCreated != 1 || trial.PublishExisting != trial.TransformAttempts-1 || trial.DerivativeFiles != 1 {
			return fmt.Errorf("valid trial %s violates multi-process contract", trial.ID)
		}
	default:
		return fmt.Errorf("valid trial %s has unknown scenario %s", trial.ID, trial.Scenario)
	}
	return nil
}

const httpStatusOK = 200

func expectedScenarios() []string {
	return []string{ScenarioS3ColdControl, ScenarioS3Cold, ScenarioS3WarmControl, ScenarioS3Warm, ScenarioF2, ScenarioS42, ScenarioS44}
}

func buildSummary(trials []analysisTrial, requests []analysisRequest) []summaryRow {
	type key struct{ scenario, class string }
	type accumulator struct {
		trials     map[string][]float64
		transforms float64
		created    float64
		trialCount int
	}
	validTrials := make(map[string]analysisTrial)
	for _, trial := range trials {
		if trial.Valid {
			validTrials[trial.ID] = trial
		}
	}
	groups := make(map[key]*accumulator)
	for _, request := range requests {
		if _, ok := validTrials[request.TrialID]; !ok || request.ErrorType != "" || request.HTTPStatus != httpStatusOK {
			continue
		}
		groupKey := key{request.Scenario, request.Class}
		group := groups[groupKey]
		if group == nil {
			group = &accumulator{trials: make(map[string][]float64)}
			groups[groupKey] = group
		}
		group.trials[request.TrialID] = append(group.trials[request.TrialID], request.LatencyMS)
	}
	for groupKey, group := range groups {
		seen := make(map[string]bool)
		for trialID := range group.trials {
			trial := validTrials[trialID]
			if !seen[trialID] {
				group.transforms += float64(trial.TransformAttempts)
				group.created += float64(trial.PublishCreated)
				group.trialCount++
				seen[trialID] = true
			}
		}
		groups[groupKey] = group
	}
	result := make([]summaryRow, 0, len(groups))
	for groupKey, group := range groups {
		row := summaryRow{Scenario: groupKey.scenario, RequestClass: groupKey.class, ValidTrials: group.trialCount}
		for _, latencies := range group.trials {
			sort.Float64s(latencies)
			row.Requests += len(latencies)
			row.P50MeanMS += percentile(latencies, 0.50)
			row.P95MeanMS += percentile(latencies, 0.95)
			row.P99MeanMS += percentile(latencies, 0.99)
		}
		if row.ValidTrials > 0 {
			denominator := float64(row.ValidTrials)
			row.P50MeanMS /= denominator
			row.P95MeanMS /= denominator
			row.P99MeanMS /= denominator
			row.TransformAttemptsMean = group.transforms / denominator
			row.PublishCreatedMean = group.created / denominator
		}
		result = append(result, row)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Scenario != result[j].Scenario {
			return result[i].Scenario < result[j].Scenario
		}
		return result[i].RequestClass < result[j].RequestClass
	})
	return result
}

func writeSummary(path string, summary []summaryRow) error {
	rows := [][]string{{"scenario", "request_class", "valid_trials", "successful_requests", "p50_mean_ms", "p95_mean_ms", "p99_mean_ms", "transform_attempts_mean", "publish_created_mean"}}
	for _, row := range summary {
		rows = append(rows, []string{
			row.Scenario, row.RequestClass, strconv.Itoa(row.ValidTrials), strconv.Itoa(row.Requests),
			formatFloat(row.P50MeanMS), formatFloat(row.P95MeanMS), formatFloat(row.P99MeanMS),
			formatFloat(row.TransformAttemptsMean), formatFloat(row.PublishCreatedMean),
		})
	}
	return writeCSV(path, rows)
}

func parseEventTime(raw string) time.Time {
	value, _ := time.Parse(time.RFC3339Nano, raw)
	return value
}

var errNoF2Events = errors.New("no F2 events")

func firstF2Events(events []map[string]string) ([]map[string]string, error) {
	var trialID string
	for _, event := range events {
		if event["scenario"] == ScenarioF2 {
			trialID = event["trial_id"]
			break
		}
	}
	if trialID == "" {
		return nil, errNoF2Events
	}
	var result []map[string]string
	for _, event := range events {
		if event["trial_id"] == trialID {
			result = append(result, event)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return parseEventTime(result[i]["timestamp"]).Before(parseEventTime(result[j]["timestamp"]))
	})
	return result, nil
}

func scenarioSummary(summary []summaryRow, scenario, class string) (summaryRow, bool) {
	for _, row := range summary {
		if row.Scenario == scenario && row.RequestClass == class {
			return row, true
		}
	}
	return summaryRow{}, false
}

func xmlEscape(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&apos;")
	return replacer.Replace(value)
}
