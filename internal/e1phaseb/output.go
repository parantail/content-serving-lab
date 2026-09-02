package e1phaseb

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
)

func writeRunOutput(output RunOutput) error {
	metadata, err := json.MarshalIndent(output.Metadata, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(output.Directory, "run.json"), append(metadata, '\n')); err != nil {
		return err
	}
	if err := writeTrials(filepath.Join(output.Directory, "trials.csv"), output.Trials); err != nil {
		return err
	}
	if err := writeRequests(filepath.Join(output.Directory, "requests.csv"), output.Requests); err != nil {
		return err
	}
	if err := writeResources(filepath.Join(output.Directory, "resources.csv"), output.Resources); err != nil {
		return err
	}
	if err := writeMetrics(filepath.Join(output.Directory, "metrics.csv"), output.Metrics); err != nil {
		return err
	}
	return writeEvents(filepath.Join(output.Directory, "events.csv"), output.Events)
}

func writeTrials(path string, trials []TrialResult) error {
	rows := [][]string{{
		"run_id", "trial_id", "cold_state_id", "scenario", "repetition", "concurrency", "process_count",
		"hot_requests", "unrelated_requests", "warm_unrelated", "valid", "invalid_reason", "start_skew_ms",
		"total_requests", "success_requests", "error_requests", "canceled_requests", "derivative_hits", "derivative_misses",
		"transform_attempts", "transform_max_inflight", "transform_inflight", "original_reads", "publish_created",
		"publish_existing", "publish_errors", "coalesced_requests", "active_tasks", "derivative_files", "p50_ms",
		"p95_ms", "p99_ms", "cpu_time_ms", "peak_rss_bytes", "peak_cgroup_memory_bytes",
	}}
	for _, trial := range trials {
		rows = append(rows, []string{
			trial.RunID, trial.TrialID, trial.ColdStateID, trial.Scenario, strconv.Itoa(trial.Repetition),
			strconv.Itoa(trial.Concurrency), strconv.Itoa(trial.ProcessCount), strconv.Itoa(trial.HotRequests),
			strconv.Itoa(trial.UnrelatedRequests), strconv.FormatBool(trial.WarmUnrelated), strconv.FormatBool(trial.Valid),
			trial.InvalidReason, formatFloat(trial.StartSkewMS), strconv.Itoa(trial.TotalRequests), strconv.Itoa(trial.SuccessRequests),
			strconv.Itoa(trial.ErrorRequests), strconv.Itoa(trial.CanceledRequests), strconv.FormatInt(trial.DerivativeHits, 10),
			strconv.FormatInt(trial.DerivativeMisses, 10), strconv.FormatInt(trial.TransformAttempts, 10),
			strconv.FormatInt(trial.TransformMaxInflight, 10), strconv.FormatInt(trial.TransformInflight, 10),
			strconv.FormatInt(trial.OriginalReads, 10), strconv.FormatInt(trial.PublishCreated, 10),
			strconv.FormatInt(trial.PublishExisting, 10), strconv.FormatInt(trial.PublishErrors, 10),
			strconv.FormatInt(trial.CoalescedRequests, 10), strconv.Itoa(trial.ActiveTasks), strconv.Itoa(trial.DerivativeFiles),
			formatFloat(trial.P50MS), formatFloat(trial.P95MS), formatFloat(trial.P99MS), formatFloat(trial.CPUTimeMS),
			strconv.FormatInt(trial.PeakRSSBytes, 10), strconv.FormatInt(trial.PeakCgroupMemBytes, 10),
		})
	}
	return writeCSV(path, rows)
}

func writeRequests(path string, requests []RequestResult) error {
	rows := [][]string{{
		"run_id", "trial_id", "scenario", "request_id", "request_class", "request_role", "target_task_id", "task_id",
		"image_key", "started_at", "finished_at", "latency_ms", "http_status", "response_sha256", "error_type", "coalesced", "cache",
	}}
	for _, request := range requests {
		rows = append(rows, []string{
			request.RunID, request.TrialID, request.Scenario, request.RequestID, request.RequestClass, request.RequestRole,
			request.TargetTaskID, request.TaskID, request.ImageKey, request.StartedAt, request.FinishedAt, formatFloat(request.LatencyMS),
			strconv.Itoa(request.HTTPStatus), request.ResponseSHA256, request.ErrorType, strconv.FormatBool(request.Coalesced), request.Cache,
		})
	}
	return writeCSV(path, rows)
}

func writeResources(path string, resources []ResourceSample) error {
	rows := [][]string{{"run_id", "trial_id", "timestamp", "elapsed_ms", "cpu_usage_usec", "rss_bytes", "cgroup_memory_bytes"}}
	for _, sample := range resources {
		rows = append(rows, []string{
			sample.RunID, sample.TrialID, sample.Timestamp, formatFloat(sample.ElapsedMS), strconv.FormatInt(sample.CPUUsageUsec, 10),
			strconv.FormatInt(sample.RSSBytes, 10), strconv.FormatInt(sample.CgroupMemoryBytes, 10),
		})
	}
	return writeCSV(path, rows)
}

func writeMetrics(path string, metrics []MetricRecord) error {
	rows := [][]string{{
		"trial_id", "scenario", "task_id", "derivative_hits", "derivative_misses", "original_success", "original_error",
		"transform_success", "transform_error", "transform_timeout", "transform_inflight", "transform_max_inflight",
		"coalesced", "publish_created", "publish_existing", "publish_error",
	}}
	for _, record := range metrics {
		snapshot := record.Snapshot
		rows = append(rows, []string{
			record.TrialID, record.Scenario, record.TaskID, strconv.FormatInt(snapshot.DerivativeHits, 10),
			strconv.FormatInt(snapshot.DerivativeMisses, 10), strconv.FormatInt(snapshot.OriginalSuccess, 10),
			strconv.FormatInt(snapshot.OriginalError, 10), strconv.FormatInt(snapshot.TransformSuccess, 10),
			strconv.FormatInt(snapshot.TransformError, 10), strconv.FormatInt(snapshot.TransformTimeout, 10),
			strconv.FormatInt(snapshot.TransformInflight, 10), strconv.FormatInt(snapshot.TransformMaxInflight, 10),
			strconv.FormatInt(snapshot.Coalesced, 10), strconv.FormatInt(snapshot.PublishCreated, 10),
			strconv.FormatInt(snapshot.PublishExisting, 10), strconv.FormatInt(snapshot.PublishError, 10),
		})
	}
	return writeCSV(path, rows)
}

func writeEvents(path string, events []Event) error {
	rows := [][]string{{"run_id", "trial_id", "scenario", "name", "request_id", "timestamp", "detail"}}
	for _, event := range events {
		rows = append(rows, []string{event.RunID, event.TrialID, event.Scenario, event.Name, event.RequestID, event.Timestamp, event.Detail})
	}
	return writeCSV(path, rows)
}

func writeCSV(path string, rows [][]string) error {
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	if err := writer.WriteAll(rows); err != nil {
		return err
	}
	return writeAtomic(path, buffer.Bytes())
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', 6, 64)
}

func readCSV(path string) ([]map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := csv.NewReader(file)
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	header := rows[0]
	result := make([]map[string]string, 0, len(rows)-1)
	for _, row := range rows[1:] {
		values := make(map[string]string, len(header))
		for index, name := range header {
			if index < len(row) {
				values[name] = row[index]
			}
		}
		result = append(result, values)
	}
	return result, nil
}
