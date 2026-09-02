package e1runner

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

type SummaryRow struct {
	Scenario              string
	Mode                  string
	Concurrency           int
	ValidTrials           int
	InvalidTrials         int
	TransformAttemptsMean float64
	TransformAttemptsMin  int64
	TransformAttemptsMax  int64
	P50MeanMS             float64
	P95MeanMS             float64
	P99MeanMS             float64
	ErrorRateMean         float64
	CPUTimeMeanMS         float64
	PeakCgroupMemMean     float64
}

type AnalysisOutput struct {
	Directory     string
	RunID         string
	Calibration   bool
	TotalTrials   int
	ValidTrials   int
	InvalidTrials int
	F1Valid       bool
	Summary       []SummaryRow
}

type analysisManifest struct {
	SchemaVersion      string   `json:"schema_version"`
	RunID              string   `json:"run_id"`
	Calibration        bool     `json:"calibration"`
	RawValidation      string   `json:"raw_validation"`
	TotalTrials        int      `json:"total_trials"`
	ValidTrials        int      `json:"valid_trials"`
	InvalidTrials      int      `json:"invalid_trials"`
	F1Valid            bool     `json:"f1_valid"`
	AggregationNote    string   `json:"aggregation_note"`
	Files              []string `json:"files"`
	SourceRuns         []string `json:"source_runs,omitempty"`
	SourceCompatibility string  `json:"source_compatibility,omitempty"`
	SelectionRule      string   `json:"selection_rule,omitempty"`
	SelectedTrials     []string `json:"selected_trials,omitempty"`
	InvalidTrialsList  []string `json:"invalid_trials_list,omitempty"`
	SurplusValidTrials []string `json:"surplus_valid_trials,omitempty"`
}

func Analyze(runDirectory string) (AnalysisOutput, error) {
	metadata, err := readRunMetadata(filepath.Join(runDirectory, "run.json"))
	if err != nil {
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
	metricAttempts, metricCoalesced, err := readMetricTotals(filepath.Join(runDirectory, "metrics.prom"))
	if err != nil {
		return AnalysisOutput{}, err
	}
	if err := validateRawResults(trials, requests, metricAttempts, metricCoalesced); err != nil {
		return AnalysisOutput{}, err
	}

	output := AnalysisOutput{
		Directory:   filepath.Join(runDirectory, "analysis"),
		RunID:       metadata.RunID,
		Calibration: metadata.Calibration,
		TotalTrials: len(trials),
	}
	for _, trial := range trials {
		if trial.Valid {
			output.ValidTrials++
			if trial.Scenario == "F1" {
				output.F1Valid = true
			}
		} else {
			output.InvalidTrials++
		}
	}
	output.Summary = summarizeForAnalysis(trials)
	if err := os.MkdirAll(output.Directory, 0o755); err != nil {
		return AnalysisOutput{}, err
	}
	if err := writeSummaryCSV(filepath.Join(output.Directory, "summary.csv"), output.Summary); err != nil {
		return AnalysisOutput{}, err
	}
	if err := writeFileAtomic(filepath.Join(output.Directory, "transform-count.svg"), transformCountSVG(output)); err != nil {
		return AnalysisOutput{}, err
	}
	if err := writeFileAtomic(filepath.Join(output.Directory, "latency-error.svg"), latencyErrorSVG(output)); err != nil {
		return AnalysisOutput{}, err
	}
	manifest := analysisManifest{
		SchemaVersion:   SchemaVersion,
		RunID:           output.RunID,
		Calibration:     output.Calibration,
		RawValidation:   "passed",
		TotalTrials:     output.TotalTrials,
		ValidTrials:     output.ValidTrials,
		InvalidTrials:   output.InvalidTrials,
		F1Valid:         output.F1Valid,
		AggregationNote: "Means and ranges are calculated from per-trial summaries; request samples are not pooled across trials.",
		Files:           []string{"summary.csv", "transform-count.svg", "latency-error.svg"},
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return AnalysisOutput{}, err
	}
	data = append(data, '\n')
	if err := writeFileAtomic(filepath.Join(output.Directory, "analysis.json"), data); err != nil {
		return AnalysisOutput{}, err
	}
	return output, nil
}

func AnalyzeSet(runDirectories []string, outputDirectory, analysisID string, validTrialsPerScenario int) (AnalysisOutput, error) {
	if len(runDirectories) == 0 {
		return AnalysisOutput{}, errors.New("analyze set requires at least one run directory")
	}
	if outputDirectory == "" || analysisID == "" || validTrialsPerScenario < 1 {
		return AnalysisOutput{}, errors.New("analyze set requires output directory, analysis ID and a positive valid trial limit")
	}
	var sourceTrials []TrialResult
	var sourceRuns []string
	var referenceMetadata *RunMetadata
	for _, directory := range runDirectories {
		validated, err := Analyze(directory)
		if err != nil {
			return AnalysisOutput{}, fmt.Errorf("validate source run %s: %w", directory, err)
		}
		trials, err := readTrials(filepath.Join(directory, "trials.csv"))
		if err != nil {
			return AnalysisOutput{}, err
		}
		metadata, err := readRunMetadata(filepath.Join(directory, "run.json"))
		if err != nil {
			return AnalysisOutput{}, err
		}
		if metadata.Calibration {
			return AnalysisOutput{}, fmt.Errorf("source run %q is calibration data", metadata.RunID)
		}
		if referenceMetadata == nil {
			referenceMetadata = &metadata
		} else if err := validateCompatibleRunMetadata(*referenceMetadata, metadata); err != nil {
			return AnalysisOutput{}, err
		}
		sourceRuns = append(sourceRuns, validated.RunID)
		sourceTrials = append(sourceTrials, trials...)
	}
	selected, selectedRefs, invalidRefs, surplusRefs, err := selectTrialsForSet(sourceTrials, validTrialsPerScenario)
	if err != nil {
		return AnalysisOutput{}, err
	}
	if err := validateRetainedScenarioSet(selected, validTrialsPerScenario); err != nil {
		return AnalysisOutput{}, err
	}
	output := AnalysisOutput{
		Directory:   outputDirectory,
		RunID:       analysisID,
		TotalTrials: len(selected),
		Summary:     summarizeForAnalysis(selected),
	}
	for _, trial := range selected {
		if trial.Valid {
			output.ValidTrials++
			if trial.Scenario == "F1" {
				output.F1Valid = true
			}
		} else {
			output.InvalidTrials++
		}
	}
	if err := os.MkdirAll(output.Directory, 0o755); err != nil {
		return AnalysisOutput{}, err
	}
	if err := writeSummaryCSV(filepath.Join(output.Directory, "summary.csv"), output.Summary); err != nil {
		return AnalysisOutput{}, err
	}
	if err := writeFileAtomic(filepath.Join(output.Directory, "transform-count.svg"), transformCountSVG(output)); err != nil {
		return AnalysisOutput{}, err
	}
	if err := writeFileAtomic(filepath.Join(output.Directory, "latency-error.svg"), latencyErrorSVG(output)); err != nil {
		return AnalysisOutput{}, err
	}
	manifest := analysisManifest{
		SchemaVersion:      SchemaVersion,
		RunID:              output.RunID,
		RawValidation:      "passed",
		TotalTrials:        output.TotalTrials,
		ValidTrials:        output.ValidTrials,
		InvalidTrials:      output.InvalidTrials,
		F1Valid:            output.F1Valid,
		AggregationNote:    "Means and ranges are calculated from per-trial summaries; request samples are not pooled across trials.",
		Files:              []string{"summary.csv", "transform-count.svg", "latency-error.svg"},
		SourceRuns:         sourceRuns,
		SourceCompatibility: "passed: commit, image, runtime, fixture, transform, limits and sampling settings match",
		SelectionRule:      fmt.Sprintf("In source-run order, select the first %d valid trials per success scenario and the first valid F1 trial; preserve every invalid trial; do not use surplus valid trials.", validTrialsPerScenario),
		SelectedTrials:     selectedRefs,
		InvalidTrialsList:  invalidRefs,
		SurplusValidTrials: surplusRefs,
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return AnalysisOutput{}, err
	}
	data = append(data, '\n')
	if err := writeFileAtomic(filepath.Join(output.Directory, "analysis.json"), data); err != nil {
		return AnalysisOutput{}, err
	}
	return output, nil
}

func validateCompatibleRunMetadata(reference, candidate RunMetadata) error {
	fields := []struct {
		name      string
		reference any
		candidate any
	}{
		{"schema_version", reference.SchemaVersion, candidate.SchemaVersion},
		{"git_commit", reference.GitCommit, candidate.GitCommit},
		{"container_image", reference.ContainerImage, candidate.ContainerImage},
		{"go_version", reference.GoVersion, candidate.GoVersion},
		{"os", reference.OS, candidate.OS},
		{"architecture", reference.Architecture, candidate.Architecture},
		{"transformer", reference.Transformer, candidate.Transformer},
		{"fixture_sha256", reference.FixtureSHA256, candidate.FixtureSHA256},
		{"fixture_bytes", reference.FixtureBytes, candidate.FixtureBytes},
		{"source_hash", reference.SourceHash, candidate.SourceHash},
		{"canonical_spec", reference.CanonicalSpec, candidate.CanonicalSpec},
		{"derivative_width", reference.DerivativeWidth, candidate.DerivativeWidth},
		{"derivative_height", reference.DerivativeHeight, candidate.DerivativeHeight},
		{"derivative_format", reference.DerivativeFormat, candidate.DerivativeFormat},
		{"derivative_quality", reference.DerivativeQuality, candidate.DerivativeQuality},
		{"transform_concurrency", reference.TransformConcurrency, candidate.TransformConcurrency},
		{"request_timeout_ms", reference.RequestTimeoutMS, candidate.RequestTimeoutMS},
		{"transform_timeout_ms", reference.TransformTimeoutMS, candidate.TransformTimeoutMS},
		{"start_skew_limit_ms", reference.StartSkewLimitMS, candidate.StartSkewLimitMS},
		{"resource_sample_gap_ms", reference.ResourceSampleGapMS, candidate.ResourceSampleGapMS},
		{"cpu_quota", reference.CPUQuota, candidate.CPUQuota},
		{"memory_limit_bytes", reference.MemoryLimitBytes, candidate.MemoryLimitBytes},
	}
	for _, field := range fields {
		if !reflect.DeepEqual(field.reference, field.candidate) {
			return fmt.Errorf("source runs %q and %q are incompatible: %s differs (%v != %v)", reference.RunID, candidate.RunID, field.name, field.reference, field.candidate)
		}
	}
	return nil
}

func validateRetainedScenarioSet(selected []TrialResult, validTrialsPerScenario int) error {
	expected := []struct {
		key  string
		want int
	}{
		{"S0\x00none", validTrialsPerScenario},
		{"S1-10\x00none", validTrialsPerScenario},
		{"S1-50\x00none", validTrialsPerScenario},
		{"S1-100\x00none", validTrialsPerScenario},
		{"S2-10\x00process-singleflight", validTrialsPerScenario},
		{"S2-50\x00process-singleflight", validTrialsPerScenario},
		{"S2-100\x00process-singleflight", validTrialsPerScenario},
		{"F1\x00process-singleflight", 1},
	}
	actual := make(map[string]int)
	for _, trial := range selected {
		if trial.Valid {
			actual[trial.Scenario+"\x00"+trial.Mode]++
		}
	}
	for _, scenario := range expected {
		if actual[scenario.key] != scenario.want {
			return fmt.Errorf("retained scenario %q has %d selected valid trials, want %d", strings.ReplaceAll(scenario.key, "\x00", "/"), actual[scenario.key], scenario.want)
		}
	}
	return nil
}

func selectTrialsForSet(source []TrialResult, validTrialsPerScenario int) ([]TrialResult, []string, []string, []string, error) {
	selected := make([]TrialResult, 0, len(source))
	selectedRefs := make([]string, 0, len(source))
	invalidRefs := make([]string, 0)
	surplusRefs := make([]string, 0)
	selectedCounts := make(map[string]int)
	observedValid := make(map[string]bool)
	for _, trial := range source {
		ref := trial.RunID + "/" + trial.TrialID
		if !trial.Valid {
			selected = append(selected, trial)
			invalidRefs = append(invalidRefs, ref)
			continue
		}
		key := trial.Scenario + "\x00" + trial.Mode
		observedValid[key] = true
		limit := validTrialsPerScenario
		if trial.Scenario == "F1" {
			limit = 1
		}
		if selectedCounts[key] >= limit {
			surplusRefs = append(surplusRefs, ref)
			continue
		}
		selectedCounts[key]++
		selected = append(selected, trial)
		selectedRefs = append(selectedRefs, ref)
	}
	for key := range observedValid {
		limit := validTrialsPerScenario
		if strings.HasPrefix(key, "F1\x00") {
			limit = 1
		}
		if selectedCounts[key] < limit {
			return nil, nil, nil, nil, fmt.Errorf("scenario %q has %d valid trials, want %d", strings.ReplaceAll(key, "\x00", "/"), selectedCounts[key], limit)
		}
	}
	return selected, selectedRefs, invalidRefs, surplusRefs, nil
}

func readRunMetadata(path string) (RunMetadata, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return RunMetadata{}, fmt.Errorf("read run metadata: %w", err)
	}
	var metadata RunMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return RunMetadata{}, fmt.Errorf("decode run metadata: %w", err)
	}
	if metadata.RunID == "" {
		return RunMetadata{}, errors.New("run metadata has no run_id")
	}
	return metadata, nil
}

func readTrials(path string) ([]TrialResult, error) {
	records, err := readCSVMaps(path)
	if err != nil {
		return nil, err
	}
	trials := make([]TrialResult, 0, len(records))
	for _, row := range records {
		trial := TrialResult{
			RunID:         row["run_id"],
			TrialID:       row["trial_id"],
			ColdStateID:   row["cold_state_id"],
			Scenario:      row["scenario"],
			Mode:          row["mode"],
			InvalidReason: row["invalid_reason"],
		}
		if err := parseFields(row, map[string]any{
			"repetition":               &trial.Repetition,
			"concurrency":              &trial.Concurrency,
			"valid":                    &trial.Valid,
			"start_skew_ms":            &trial.StartSkewMS,
			"total_requests":           &trial.TotalRequests,
			"success_requests":         &trial.SuccessRequests,
			"error_requests":           &trial.ErrorRequests,
			"transform_attempts":       &trial.TransformAttempts,
			"original_reads":           &trial.OriginalReads,
			"publish_created":          &trial.PublishCreated,
			"publish_existing":         &trial.PublishExisting,
			"coalesced_requests":       &trial.CoalescedRequests,
			"p50_ms":                   &trial.P50MS,
			"p95_ms":                   &trial.P95MS,
			"p99_ms":                   &trial.P99MS,
			"cpu_time_ms":              &trial.CPUTimeMS,
			"peak_rss_bytes":           &trial.PeakRSSBytes,
			"peak_cgroup_memory_bytes": &trial.PeakCgroupMemBytes,
		}); err != nil {
			return nil, fmt.Errorf("parse trial %q: %w", trial.TrialID, err)
		}
		if trial.TrialID == "" {
			return nil, errors.New("trial row has no trial_id")
		}
		trials = append(trials, trial)
	}
	if len(trials) == 0 {
		return nil, errors.New("trials.csv contains no trials")
	}
	return trials, nil
}

func readRequests(path string) ([]RequestResult, error) {
	records, err := readCSVMaps(path)
	if err != nil {
		return nil, err
	}
	requests := make([]RequestResult, 0, len(records))
	for _, row := range records {
		request := RequestResult{
			RunID: row["run_id"], TrialID: row["trial_id"], Scenario: row["scenario"], RequestID: row["request_id"],
			ImageKey: row["image_key"], TaskID: row["task_id"], StartedAt: row["started_at"], FinishedAt: row["finished_at"],
			ResponseSHA256: row["response_sha256"], ErrorType: row["error_type"], Cache: row["cache"],
		}
		if err := parseFields(row, map[string]any{
			"latency_ms":  &request.LatencyMS,
			"http_status": &request.HTTPStatus,
			"coalesced":   &request.Coalesced,
		}); err != nil {
			return nil, fmt.Errorf("parse request %q: %w", request.RequestID, err)
		}
		requests = append(requests, request)
	}
	return requests, nil
}

func readCSVMaps(path string) ([]map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", filepath.Base(path), err)
	}
	defer file.Close()
	reader := csv.NewReader(file)
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%s is empty", filepath.Base(path))
	}
	headers := rows[0]
	result := make([]map[string]string, 0, len(rows)-1)
	for index, values := range rows[1:] {
		if len(values) != len(headers) {
			return nil, fmt.Errorf("%s row %d has %d values, want %d", filepath.Base(path), index+2, len(values), len(headers))
		}
		row := make(map[string]string, len(headers))
		for column, header := range headers {
			row[header] = values[column]
		}
		result = append(result, row)
	}
	return result, nil
}

func parseFields(row map[string]string, fields map[string]any) error {
	for name, target := range fields {
		raw, found := row[name]
		if !found {
			return fmt.Errorf("missing column %s", name)
		}
		var err error
		switch value := target.(type) {
		case *int:
			*value, err = strconv.Atoi(raw)
		case *int64:
			*value, err = strconv.ParseInt(raw, 10, 64)
		case *float64:
			*value, err = strconv.ParseFloat(raw, 64)
		case *bool:
			*value, err = strconv.ParseBool(raw)
		default:
			return fmt.Errorf("unsupported parse target for %s", name)
		}
		if err != nil {
			return fmt.Errorf("%s=%q: %w", name, raw, err)
		}
	}
	return nil
}

func readMetricTotals(path string) (map[string]int64, map[string]int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open metrics.prom: %w", err)
	}
	defer file.Close()
	attempts := make(map[string]int64)
	coalesced := make(map[string]int64)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		isAttempts := strings.HasPrefix(line, "media_transform_attempts_total{")
		isCoalesced := strings.HasPrefix(line, "media_requests_coalesced_total{")
		if !isAttempts && !isCoalesced {
			continue
		}
		trialID, found := prometheusLabel(line, "trial_id")
		if !found {
			continue
		}
		value, err := prometheusInteger(line)
		if err != nil {
			return nil, nil, err
		}
		switch {
		case isAttempts:
			attempts[trialID] += value
		case isCoalesced:
			coalesced[trialID] += value
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, err
	}
	return attempts, coalesced, nil
}

func prometheusLabel(line, name string) (string, bool) {
	marker := name + `="`
	start := strings.Index(line, marker)
	if start < 0 {
		return "", false
	}
	start += len(marker)
	end := strings.Index(line[start:], `"`)
	if end < 0 {
		return "", false
	}
	return line[start : start+end], true
}

func prometheusInteger(line string) (int64, error) {
	separator := strings.LastIndexByte(line, ' ')
	if separator < 0 {
		return 0, fmt.Errorf("invalid metric line %q", line)
	}
	value, err := strconv.ParseInt(line[separator+1:], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid integer metric line %q: %w", line, err)
	}
	return value, nil
}

func validateRawResults(trials []TrialResult, requests []RequestResult, attempts, coalesced map[string]int64) error {
	byTrial := make(map[string][]RequestResult)
	for _, request := range requests {
		byTrial[request.TrialID] = append(byTrial[request.TrialID], request)
	}
	seen := make(map[string]bool)
	for _, trial := range trials {
		if seen[trial.TrialID] {
			return fmt.Errorf("duplicate trial_id %q", trial.TrialID)
		}
		seen[trial.TrialID] = true
		trialRequests := byTrial[trial.TrialID]
		if len(trialRequests) != trial.TotalRequests {
			return fmt.Errorf("trial %s request count %d != summary %d", trial.TrialID, len(trialRequests), trial.TotalRequests)
		}
		success := 0
		requestCoalesced := int64(0)
		latencies := make([]float64, 0, len(trialRequests))
		for _, request := range trialRequests {
			if request.HTTPStatus == 200 && request.ErrorType == "" {
				success++
			}
			if request.Coalesced {
				requestCoalesced++
			}
			latencies = append(latencies, request.LatencyMS)
		}
		if success != trial.SuccessRequests || len(trialRequests)-success != trial.ErrorRequests {
			return fmt.Errorf("trial %s success/error counts do not match requests.csv", trial.TrialID)
		}
		sort.Float64s(latencies)
		for name, values := range map[string][2]float64{
			"p50": {percentile(latencies, 0.50), trial.P50MS},
			"p95": {percentile(latencies, 0.95), trial.P95MS},
			"p99": {percentile(latencies, 0.99), trial.P99MS},
		} {
			if math.Abs(values[0]-values[1]) > 0.001 {
				return fmt.Errorf("trial %s %s %.6f != summary %.6f", trial.TrialID, name, values[0], values[1])
			}
		}
		if attempts[trial.TrialID] != trial.TransformAttempts {
			return fmt.Errorf("trial %s transform attempts metric %d != summary %d", trial.TrialID, attempts[trial.TrialID], trial.TransformAttempts)
		}
		if coalesced[trial.TrialID] != trial.CoalescedRequests {
			return fmt.Errorf("trial %s coalesced metric %d != summary %d", trial.TrialID, coalesced[trial.TrialID], trial.CoalescedRequests)
		}
		if requestCoalesced != trial.CoalescedRequests {
			return fmt.Errorf("trial %s coalesced request rows %d != summary %d", trial.TrialID, requestCoalesced, trial.CoalescedRequests)
		}
	}
	for trialID := range byTrial {
		if !seen[trialID] {
			return fmt.Errorf("requests.csv contains unknown trial_id %q", trialID)
		}
	}
	return nil
}

func summarizeForAnalysis(trials []TrialResult) []SummaryRow {
	type group struct {
		row    SummaryRow
		trials []TrialResult
	}
	groups := make(map[string]*group)
	for _, trial := range trials {
		key := fmt.Sprintf("%s\x00%s\x00%d", trial.Scenario, trial.Mode, trial.Concurrency)
		value, found := groups[key]
		if !found {
			value = &group{row: SummaryRow{Scenario: trial.Scenario, Mode: trial.Mode, Concurrency: trial.Concurrency}}
			groups[key] = value
		}
		if trial.Valid {
			value.row.ValidTrials++
			value.trials = append(value.trials, trial)
		} else {
			value.row.InvalidTrials++
		}
	}
	result := make([]SummaryRow, 0, len(groups))
	for _, value := range groups {
		if len(value.trials) > 0 {
			value.row.TransformAttemptsMin = value.trials[0].TransformAttempts
			for _, trial := range value.trials {
				value.row.TransformAttemptsMean += float64(trial.TransformAttempts)
				value.row.TransformAttemptsMin = min(value.row.TransformAttemptsMin, trial.TransformAttempts)
				value.row.TransformAttemptsMax = max(value.row.TransformAttemptsMax, trial.TransformAttempts)
				value.row.P50MeanMS += trial.P50MS
				value.row.P95MeanMS += trial.P95MS
				value.row.P99MeanMS += trial.P99MS
				value.row.ErrorRateMean += float64(trial.ErrorRequests) / float64(trial.TotalRequests)
				value.row.CPUTimeMeanMS += trial.CPUTimeMS
				value.row.PeakCgroupMemMean += float64(trial.PeakCgroupMemBytes)
			}
			count := float64(len(value.trials))
			value.row.TransformAttemptsMean /= count
			value.row.P50MeanMS /= count
			value.row.P95MeanMS /= count
			value.row.P99MeanMS /= count
			value.row.ErrorRateMean /= count
			value.row.CPUTimeMeanMS /= count
			value.row.PeakCgroupMemMean /= count
		}
		result = append(result, value.row)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Scenario != result[j].Scenario {
			return result[i].Scenario < result[j].Scenario
		}
		return result[i].Mode < result[j].Mode
	})
	return result
}

func writeSummaryCSV(path string, summary []SummaryRow) error {
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	rows := [][]string{{
		"scenario", "mode", "concurrency", "valid_trials", "invalid_trials", "transform_attempts_mean",
		"transform_attempts_min", "transform_attempts_max", "p50_mean_ms", "p95_mean_ms", "p99_mean_ms",
		"error_rate_mean", "cpu_time_mean_ms", "peak_cgroup_memory_mean_bytes",
	}}
	for _, row := range summary {
		rows = append(rows, []string{
			row.Scenario, row.Mode, strconv.Itoa(row.Concurrency), strconv.Itoa(row.ValidTrials), strconv.Itoa(row.InvalidTrials),
			formatFloat(row.TransformAttemptsMean), strconv.FormatInt(row.TransformAttemptsMin, 10), strconv.FormatInt(row.TransformAttemptsMax, 10),
			formatFloat(row.P50MeanMS), formatFloat(row.P95MeanMS), formatFloat(row.P99MeanMS), formatFloat(row.ErrorRateMean),
			formatFloat(row.CPUTimeMeanMS), formatFloat(row.PeakCgroupMemMean),
		})
	}
	if err := writer.WriteAll(rows); err != nil {
		return err
	}
	return writeFileAtomic(path, buffer.Bytes())
}
