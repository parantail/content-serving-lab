package e1awss4runner

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func writeRunOutput(output RunOutput) error {
	if err := writeJSON(filepath.Join(output.Directory, "run.json"), output.Metadata); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(output.Directory, "infrastructure.json"), output.Infrastructure); err != nil {
		return err
	}
	if err := writeTrials(filepath.Join(output.Directory, "trials.csv"), output.Trials); err != nil {
		return err
	}
	if err := writeRequests(filepath.Join(output.Directory, "requests.csv"), output.Requests); err != nil {
		return err
	}
	if err := writeTasks(filepath.Join(output.Directory, "tasks.csv"), output.Tasks); err != nil {
		return err
	}
	if err := writeResources(filepath.Join(output.Directory, "resources.csv"), output.Resources); err != nil {
		return err
	}
	if err := writeStorage(filepath.Join(output.Directory, "storage.csv"), output.Storage); err != nil {
		return err
	}
	if err := writeEvents(filepath.Join(output.Directory, "events.csv"), output.Events); err != nil {
		return err
	}
	return writeJSON(filepath.Join(output.Directory, "cost.json"), output.Cost)
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(data, '\n'))
}

func writeTrials(path string, records []TrialRecord) error {
	rows := [][]string{{
		"run_id", "trial_id", "scenario", "repetition", "expected_tasks", "deployment_stable_before", "deployment_stable_after",
		"healthy_before_task_ids", "healthy_after_task_ids", "prepared_task_ids", "finished_task_ids", "cold_confirmed",
		"total_requests", "success_requests", "http_5xx", "timeouts", "connection_errors", "start_skew_ms",
		"response_sha256", "response_bytes", "stored_sha256", "stored_bytes", "stored_webp_valid", "image_requests",
		"derivative_get_hit", "derivative_get_miss", "derivative_get_error", "derivative_get_bytes", "original_get_count",
		"original_get_bytes", "transform_attempts", "transform_success", "transform_failure", "transform_duration_nanos",
		"transform_max_inflight", "transform_inflight", "coalesced_requests", "publish_created", "publish_existing",
		"publish_conflict", "publish_error", "publish_attempt_bytes", "coordinator_keys", "coordinator_waiters",
		"cpu_usage_nanos", "peak_memory_bytes", "resource_errors", "p50_ms", "p95_ms", "p99_ms", "valid", "invalid_reason",
	}}
	for _, record := range records {
		rows = append(rows, []string{
			record.RunID, record.TrialID, record.Scenario, strconv.Itoa(record.Repetition), strconv.Itoa(record.ExpectedTasks),
			strconv.FormatBool(record.DeploymentStableBefore), strconv.FormatBool(record.DeploymentStableAfter), joinIDs(record.HealthyBeforeTaskIDs),
			joinIDs(record.HealthyAfterTaskIDs), joinIDs(record.PreparedTaskIDs), joinIDs(record.FinishedTaskIDs), strconv.FormatBool(record.ColdConfirmed),
			strconv.Itoa(record.TotalRequests), strconv.Itoa(record.SuccessRequests), strconv.Itoa(record.HTTP5xx), strconv.Itoa(record.Timeouts),
			strconv.Itoa(record.ConnectionErrors), formatFloat(record.StartSkewMS), record.ResponseSHA256, strconv.FormatInt(record.ResponseBytes, 10),
			record.StoredSHA256, strconv.FormatInt(record.StoredBytes, 10), strconv.FormatBool(record.StoredWebPValid), strconv.FormatInt(record.ImageRequests, 10),
			strconv.FormatInt(record.DerivativeGetHit, 10), strconv.FormatInt(record.DerivativeGetMiss, 10), strconv.FormatInt(record.DerivativeGetError, 10),
			strconv.FormatInt(record.DerivativeGetBytes, 10), strconv.FormatInt(record.OriginalGetCount, 10), strconv.FormatInt(record.OriginalGetBytes, 10),
			strconv.FormatInt(record.TransformAttempts, 10), strconv.FormatInt(record.TransformSuccess, 10), strconv.FormatInt(record.TransformFailure, 10),
			strconv.FormatInt(record.TransformDurationNanos, 10), strconv.FormatInt(record.TransformMaxInflight, 10), strconv.FormatInt(record.TransformInflight, 10),
			strconv.FormatInt(record.CoalescedRequests, 10), strconv.FormatInt(record.PublishCreated, 10), strconv.FormatInt(record.PublishExisting, 10),
			strconv.FormatInt(record.PublishConflict, 10), strconv.FormatInt(record.PublishError, 10), strconv.FormatInt(record.PublishAttemptBytes, 10),
			strconv.Itoa(record.CoordinatorKeys), strconv.Itoa(record.CoordinatorWaiters), strconv.FormatUint(record.CPUUsageNanos, 10),
			strconv.FormatUint(record.PeakMemoryBytes, 10), strconv.FormatInt(record.ResourceErrors, 10), formatFloat(record.P50MS),
			formatFloat(record.P95MS), formatFloat(record.P99MS), strconv.FormatBool(record.Valid), record.InvalidReason,
		})
	}
	return writeCSV(path, rows)
}

func writeRequests(path string, records []RequestRecord) error {
	rows := [][]string{{
		"run_id", "trial_id", "scenario", "request_id", "task_id", "availability_zone", "started_at", "finished_at",
		"latency_ms", "http_status", "response_bytes", "response_sha256", "derivative_key", "cache", "coalesced", "trial_header", "error_type",
	}}
	for _, record := range records {
		rows = append(rows, []string{
			record.RunID, record.TrialID, record.Scenario, record.RequestID, record.TaskID, record.AvailabilityZone,
			record.StartedAt, record.FinishedAt, formatFloat(record.LatencyMS), strconv.Itoa(record.HTTPStatus),
			strconv.FormatInt(record.ResponseBytes, 10), record.ResponseSHA256, record.DerivativeKey, record.Cache,
			strconv.FormatBool(record.Coalesced), record.TrialHeader, record.ErrorType,
		})
	}
	return writeCSV(path, rows)
}

func writeTasks(path string, records []TaskRecord) error {
	rows := [][]string{{
		"run_id", "trial_id", "scenario", "task_id", "task_definition_family", "task_definition_revision", "availability_zone",
		"launch_type", "cpu_vcpu", "memory_mib", "image_digest", "prepared_at", "finished_at", "first_request_at", "last_request_at",
		"image_requests", "requests_inflight", "derivative_get_hit", "derivative_get_miss", "derivative_get_error", "derivative_get_bytes",
		"original_get_count", "original_get_success", "original_get_error", "original_get_bytes", "transform_attempts", "transform_success",
		"transform_failure", "transform_error", "transform_timeout", "transform_duration_nanos", "transform_inflight", "transform_max_inflight",
		"coalesced_requests", "publish_created", "publish_existing", "publish_conflict", "publish_error", "publish_attempt_bytes",
		"coordinator_keys", "coordinator_waiters", "cpu_usage_nanos", "peak_memory_bytes", "resource_errors",
	}}
	for _, record := range records {
		task, counter := record.Task, record.Counters
		rows = append(rows, []string{
			record.RunID, record.TrialID, record.Scenario, task.TaskID, task.TaskDefinitionFamily, task.TaskDefinitionRevision,
			task.AvailabilityZone, task.LaunchType, formatFloat(task.CPUVCpu), strconv.FormatInt(task.MemoryMiB, 10), task.ImageDigest,
			record.PreparedAt, record.FinishedAt, record.FirstRequestAt, record.LastRequestAt,
			strconv.FormatInt(counter.ImageRequests, 10), strconv.FormatInt(counter.RequestsInflight, 10),
			strconv.FormatInt(counter.DerivativeGetHit, 10), strconv.FormatInt(counter.DerivativeGetMiss, 10),
			strconv.FormatInt(counter.DerivativeGetError, 10), strconv.FormatInt(counter.DerivativeGetBytes, 10),
			strconv.FormatInt(counter.OriginalGetCount, 10), strconv.FormatInt(counter.OriginalGetSuccess, 10),
			strconv.FormatInt(counter.OriginalGetError, 10), strconv.FormatInt(counter.OriginalGetBytes, 10),
			strconv.FormatInt(counter.TransformAttempts, 10), strconv.FormatInt(counter.TransformSuccess, 10),
			strconv.FormatInt(counter.TransformFailure, 10), strconv.FormatInt(counter.TransformError, 10),
			strconv.FormatInt(counter.TransformTimeout, 10), strconv.FormatInt(counter.TransformDurationNanos, 10),
			strconv.FormatInt(counter.TransformInflight, 10), strconv.FormatInt(counter.TransformMaxInflight, 10),
			strconv.FormatInt(counter.CoalescedRequests, 10), strconv.FormatInt(counter.PublishCreated, 10),
			strconv.FormatInt(counter.PublishExisting, 10), strconv.FormatInt(counter.PublishConflict, 10),
			strconv.FormatInt(counter.PublishError, 10), strconv.FormatInt(counter.PublishAttemptBytes, 10),
			strconv.Itoa(counter.CoordinatorKeys), strconv.Itoa(counter.CoordinatorWaiters), strconv.FormatUint(counter.CPUUsageNanos, 10),
			strconv.FormatUint(counter.PeakMemoryBytes, 10), strconv.FormatInt(counter.ResourceErrors, 10),
		})
	}
	return writeCSV(path, rows)
}

func writeResources(path string, records []ResourceRecord) error {
	rows := [][]string{{"run_id", "trial_id", "scenario", "task_id", "timestamp", "cpu_usage_nanos", "memory_usage_bytes", "error"}}
	for _, record := range records {
		rows = append(rows, []string{
			record.RunID, record.TrialID, record.Scenario, record.TaskID, record.Timestamp,
			strconv.FormatUint(record.CPUUsageNanos, 10), strconv.FormatUint(record.MemoryUsageBytes, 10), record.Error,
		})
	}
	return writeCSV(path, rows)
}

func writeStorage(path string, records []StorageRecord) error {
	rows := [][]string{{"run_id", "trial_id", "scenario", "actor", "task_id", "operation", "result", "requests", "bytes"}}
	for _, record := range records {
		rows = append(rows, []string{
			record.RunID, record.TrialID, record.Scenario, record.Actor, record.TaskID, record.Operation,
			record.Result, strconv.FormatInt(record.Requests, 10), strconv.FormatInt(record.Bytes, 10),
		})
	}
	return writeCSV(path, rows)
}

func writeEvents(path string, records []EventRecord) error {
	rows := [][]string{{"run_id", "trial_id", "scenario", "name", "timestamp", "detail"}}
	for _, record := range records {
		rows = append(rows, []string{record.RunID, record.TrialID, record.Scenario, record.Name, record.Timestamp, record.Detail})
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
	if len(rows) == 0 || len(rows[0]) == 0 {
		return nil, errors.New("CSV is empty")
	}
	header := rows[0]
	seen := make(map[string]bool, len(header))
	for _, name := range header {
		if name == "" || seen[name] {
			return nil, errors.New("CSV has an empty or duplicate header")
		}
		seen[name] = true
	}
	result := make([]map[string]string, 0, len(rows)-1)
	for index, row := range rows[1:] {
		if len(row) != len(header) {
			return nil, fmt.Errorf("CSV row %d has %d columns, want %d", index+2, len(row), len(header))
		}
		values := make(map[string]string, len(header))
		for column, name := range header {
			values[name] = row[column]
		}
		result = append(result, values)
	}
	return result, nil
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".e1-aws-s4-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', 6, 64)
}

func joinIDs(values []string) string {
	return strings.Join(sortedStrings(values), ";")
}

func splitIDs(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, ";")
}
