package e1runner

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

func writeRunOutput(output RunOutput) error {
	metadata, err := json.MarshalIndent(output.Metadata, "", "  ")
	if err != nil {
		return err
	}
	metadata = append(metadata, '\n')
	if err := writeFileAtomic(filepath.Join(output.Directory, "run.json"), metadata); err != nil {
		return err
	}
	if err := writeTrialsCSV(filepath.Join(output.Directory, "trials.csv"), output.Trials); err != nil {
		return err
	}
	if err := writeRequestsCSV(filepath.Join(output.Directory, "requests.csv"), output.Requests); err != nil {
		return err
	}
	if err := writeResourcesCSV(filepath.Join(output.Directory, "resources.csv"), output.Resources); err != nil {
		return err
	}
	if err := writeMetrics(filepath.Join(output.Directory, "metrics.prom"), output.Metrics); err != nil {
		return err
	}
	return writeLogs(filepath.Join(output.Directory, "logs.jsonl"), output.Requests)
}

func writeTrialsCSV(path string, trials []TrialResult) error {
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	rows := [][]string{{
		"run_id", "trial_id", "cold_state_id", "scenario", "mode", "repetition", "concurrency", "valid", "invalid_reason",
		"start_skew_ms", "total_requests", "success_requests", "error_requests", "transform_attempts",
		"original_reads", "publish_created", "publish_existing", "coalesced_requests", "p50_ms", "p95_ms",
		"p99_ms", "cpu_time_ms", "peak_rss_bytes", "peak_cgroup_memory_bytes",
	}}
	for _, trial := range trials {
		rows = append(rows, []string{
			trial.RunID,
			trial.TrialID,
			trial.ColdStateID,
			trial.Scenario,
			trial.Mode,
			strconv.Itoa(trial.Repetition),
			strconv.Itoa(trial.Concurrency),
			strconv.FormatBool(trial.Valid),
			trial.InvalidReason,
			formatFloat(trial.StartSkewMS),
			strconv.Itoa(trial.TotalRequests),
			strconv.Itoa(trial.SuccessRequests),
			strconv.Itoa(trial.ErrorRequests),
			strconv.FormatInt(trial.TransformAttempts, 10),
			strconv.FormatInt(trial.OriginalReads, 10),
			strconv.FormatInt(trial.PublishCreated, 10),
			strconv.FormatInt(trial.PublishExisting, 10),
			strconv.FormatInt(trial.CoalescedRequests, 10),
			formatFloat(trial.P50MS),
			formatFloat(trial.P95MS),
			formatFloat(trial.P99MS),
			formatFloat(trial.CPUTimeMS),
			strconv.FormatInt(trial.PeakRSSBytes, 10),
			strconv.FormatInt(trial.PeakCgroupMemBytes, 10),
		})
	}
	if err := writer.WriteAll(rows); err != nil {
		return err
	}
	return writeFileAtomic(path, buffer.Bytes())
}

func writeRequestsCSV(path string, requests []RequestResult) error {
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	rows := [][]string{{
		"run_id", "trial_id", "scenario", "request_id", "image_key", "task_id", "started_at", "finished_at",
		"latency_ms", "http_status", "response_sha256", "error_type", "coalesced", "cache",
	}}
	for _, request := range requests {
		rows = append(rows, []string{
			request.RunID,
			request.TrialID,
			request.Scenario,
			request.RequestID,
			request.ImageKey,
			request.TaskID,
			request.StartedAt,
			request.FinishedAt,
			formatFloat(request.LatencyMS),
			strconv.Itoa(request.HTTPStatus),
			request.ResponseSHA256,
			request.ErrorType,
			strconv.FormatBool(request.Coalesced),
			request.Cache,
		})
	}
	if err := writer.WriteAll(rows); err != nil {
		return err
	}
	return writeFileAtomic(path, buffer.Bytes())
}

func writeResourcesCSV(path string, resources []ResourceSample) error {
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	rows := [][]string{{
		"run_id", "trial_id", "timestamp", "elapsed_ms", "cpu_usage_usec", "rss_bytes", "cgroup_memory_bytes",
	}}
	for _, sample := range resources {
		rows = append(rows, []string{
			sample.RunID,
			sample.TrialID,
			sample.Timestamp,
			formatFloat(sample.ElapsedMS),
			strconv.FormatInt(sample.CPUUsageUsec, 10),
			strconv.FormatInt(sample.RSSBytes, 10),
			strconv.FormatInt(sample.CgroupMemoryBytes, 10),
		})
	}
	if err := writer.WriteAll(rows); err != nil {
		return err
	}
	return writeFileAtomic(path, buffer.Bytes())
}

func writeMetrics(path string, metrics []trialMetrics) error {
	var buffer bytes.Buffer
	for _, trial := range metrics {
		labels := fmt.Sprintf(`trial_id=%q,scenario=%q`, trial.TrialID, trial.Scenario)
		writeMetric := func(name, extraLabel string, value int64) {
			allLabels := labels
			if extraLabel != "" {
				allLabels += "," + extraLabel
			}
			fmt.Fprintf(&buffer, "%s{%s} %d\n", name, allLabels, value)
		}
		snapshot := trial.Snapshot
		writeMetric("media_derivative_requests_total", `result="hit"`, snapshot.DerivativeHits)
		writeMetric("media_derivative_requests_total", `result="miss"`, snapshot.DerivativeMisses)
		writeMetric("media_original_reads_total", `result="success"`, snapshot.OriginalSuccess)
		writeMetric("media_original_reads_total", `result="error"`, snapshot.OriginalError)
		writeMetric("media_transform_attempts_total", `result="success"`, snapshot.TransformSuccess)
		writeMetric("media_transform_attempts_total", `result="error"`, snapshot.TransformError)
		writeMetric("media_transform_attempts_total", `result="timeout"`, snapshot.TransformTimeout)
		writeMetric("media_requests_coalesced_total", "", snapshot.Coalesced)
		writeMetric("media_derivative_publish_attempts_total", `result="created"`, snapshot.PublishCreated)
		writeMetric("media_derivative_publish_attempts_total", `result="existing"`, snapshot.PublishExisting)
		writeMetric("media_derivative_publish_attempts_total", `result="error"`, snapshot.PublishError)
		fmt.Fprintf(
			&buffer,
			"media_transform_duration_seconds_sum{%s} %.9f\n",
			labels,
			float64(snapshot.TransformDurationNanos)/1e9,
		)
		fmt.Fprintf(&buffer, "media_transform_duration_seconds_count{%s} %d\n", labels, snapshot.TransformSuccess+snapshot.TransformError+snapshot.TransformTimeout)
		requestDurations := []struct {
			cache, result string
			count, nanos  int64
		}{
			{"miss", "success", snapshot.RequestMissSuccessCount, snapshot.RequestMissSuccessNanos},
			{"miss", "error", snapshot.RequestMissErrorCount, snapshot.RequestMissErrorNanos},
			{"derivative", "success", snapshot.RequestHitSuccessCount, snapshot.RequestHitSuccessNanos},
			{"derivative", "error", snapshot.RequestHitErrorCount, snapshot.RequestHitErrorNanos},
		}
		for _, value := range requestDurations {
			requestLabels := labels + fmt.Sprintf(`,cache=%q,result=%q`, value.cache, value.result)
			fmt.Fprintf(&buffer, "media_request_duration_seconds_sum{%s} %.9f\n", requestLabels, float64(value.nanos)/1e9)
			fmt.Fprintf(&buffer, "media_request_duration_seconds_count{%s} %d\n", requestLabels, value.count)
		}
	}
	return writeFileAtomic(path, buffer.Bytes())
}

func writeLogs(path string, requests []RequestResult) error {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	for _, request := range requests {
		level := "info"
		if request.ErrorType != "" {
			level = "error"
		}
		entry := map[string]any{
			"timestamp":   request.FinishedAt,
			"level":       level,
			"event":       "derivative_request_finished",
			"run_id":      request.RunID,
			"trial_id":    request.TrialID,
			"scenario":    request.Scenario,
			"request_id":  request.RequestID,
			"image_key":   request.ImageKey,
			"latency_ms":  request.LatencyMS,
			"http_status": request.HTTPStatus,
			"error_type":  request.ErrorType,
			"coalesced":   request.Coalesced,
		}
		if err := encoder.Encode(entry); err != nil {
			return err
		}
	}
	return writeFileAtomic(path, buffer.Bytes())
}

func writeFileAtomic(path string, data []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".result-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(data); err != nil {
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
