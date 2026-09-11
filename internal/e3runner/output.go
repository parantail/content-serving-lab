package e3runner

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

var requestColumns = []string{
	"run_id", "trial_id", "mode", "fault", "repetition", "stream", "request_id", "source_hash", "spec", "image_key",
	"started_at", "finished_at", "offset_ms", "phase", "latency_ms", "http_status", "cache", "coalesced",
	"isolation", "retry_after", "error_type", "response_sha256",
}

var stateColumns = []string{
	"run_id", "trial_id", "timestamp", "offset_ms", "fault_kind", "kill_switch_enabled", "transform_inflight",
	"transform_waiting", "coordinator_keys", "coordinator_waiters", "original_bytes_inflight", "derivative_hits",
	"derivative_misses", "transform_success", "transform_error", "transform_timeout", "transform_shed",
	"kill_switch_rejected", "fault_injections",
}

var resourceColumns = []string{
	"run_id", "trial_id", "timestamp", "elapsed_ms", "cpu_usage_usec", "rss_bytes", "cgroup_memory_bytes",
}

var trialColumns = []string{
	"run_id", "trial_id", "mode", "fault", "repetition", "valid", "invalid_reason", "started_at", "finished_at",
	"total_requests", "success_requests", "error_requests", "shed_requests", "kill_switch_requests", "saturated_requests",
	"fault_on_offset_ms", "fault_off_offset_ms", "kill_switch_on_offset_ms", "kill_switch_off_offset_ms",
	"derivative_hits", "derivative_misses", "transform_success", "transform_error", "transform_timeout",
	"transform_shed", "kill_switch_rejected", "fault_injections", "max_transform_waiting", "max_original_bytes_inflight",
	"cpu_time_ms", "peak_rss_bytes", "peak_cgroup_memory_bytes",
	"hit_normal_p99_ms", "hit_fault_p99_ms", "hit_recovery_p99_ms", "hit_fault_errors", "hit_fault_requests",
	"miss_normal_p99_ms", "miss_fault_p99_ms", "miss_recovery_p99_ms", "miss_fault_errors", "miss_fault_requests",
	"poisoned_fault_errors", "poisoned_fault_requests",
}

// rawWriter appends per-trial rows to the large raw files so a long run does
// not rewrite them after every trial.
type rawWriter struct {
	requests  *appendCSV
	states    *appendCSV
	resources *appendCSV
	events    *os.File
}

// appendCSV streams rows to a CSV file; the two large raw files are gzip
// compressed so a full run stays well below repository file-size limits.
// Every trial ends with a flush and fsync so a crash keeps earlier trials.
type appendCSV struct {
	file   *os.File
	gzip   *gzip.Writer
	writer *csv.Writer
}

func openAppendCSV(path string, columns []string, compress bool) (*appendCSV, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	a := &appendCSV{file: file}
	if compress {
		a.gzip = gzip.NewWriter(file)
		a.writer = csv.NewWriter(a.gzip)
	} else {
		a.writer = csv.NewWriter(file)
	}
	if err := a.writer.Write(columns); err != nil {
		file.Close()
		return nil, err
	}
	return a, a.flush()
}

func (a *appendCSV) writeAll(rows [][]string) error {
	for _, row := range rows {
		if err := a.writer.Write(row); err != nil {
			return err
		}
	}
	return a.flush()
}

func (a *appendCSV) flush() error {
	a.writer.Flush()
	if err := a.writer.Error(); err != nil {
		return err
	}
	if a.gzip != nil {
		if err := a.gzip.Flush(); err != nil {
			return err
		}
	}
	return a.file.Sync()
}

func (a *appendCSV) close() error {
	if a.gzip != nil {
		if err := a.gzip.Close(); err != nil {
			a.file.Close()
			return err
		}
	}
	return a.file.Close()
}

const (
	requestsFile  = "requests.csv.gz"
	resourcesFile = "resources.csv.gz"
	stateFile     = "state.csv"
)

func openRawWriter(directory string) (*rawWriter, error) {
	requests, err := openAppendCSV(filepath.Join(directory, requestsFile), requestColumns, true)
	if err != nil {
		return nil, err
	}
	states, err := openAppendCSV(filepath.Join(directory, stateFile), stateColumns, false)
	if err != nil {
		return nil, err
	}
	resources, err := openAppendCSV(filepath.Join(directory, resourcesFile), resourceColumns, true)
	if err != nil {
		return nil, err
	}
	events, err := os.OpenFile(filepath.Join(directory, "logs.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &rawWriter{requests: requests, states: states, resources: resources, events: events}, nil
}

func (w *rawWriter) appendTrial(requests []RequestResult, events []Event, states []StateSample, resources []ResourceSample) error {
	if err := w.requests.writeAll(requestRows(requests)); err != nil {
		return err
	}
	if err := w.states.writeAll(stateRows(states)); err != nil {
		return err
	}
	if err := w.resources.writeAll(resourceRows(resources)); err != nil {
		return err
	}
	buffered := bufio.NewWriter(w.events)
	encoder := json.NewEncoder(buffered)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			return err
		}
	}
	if err := buffered.Flush(); err != nil {
		return err
	}
	return w.events.Sync()
}

func (w *rawWriter) close() {
	_ = w.requests.close()
	_ = w.states.close()
	_ = w.resources.close()
	_ = w.events.Close()
}

func requestRows(requests []RequestResult) [][]string {
	rows := make([][]string, 0, len(requests))
	for _, request := range requests {
		rows = append(rows, []string{
			request.RunID, request.TrialID, request.Mode, request.Fault, strconv.Itoa(request.Repetition),
			request.Stream, request.RequestID, request.SourceHash, request.Spec, request.ImageKey,
			request.StartedAt, request.FinishedAt, formatFloat(request.OffsetMS), request.Phase,
			formatFloat(request.LatencyMS), strconv.Itoa(request.HTTPStatus), request.Cache,
			strconv.FormatBool(request.Coalesced), request.Isolation, request.RetryAfter, request.ErrorType,
			request.ResponseSHA256,
		})
	}
	return rows
}

func stateRows(states []StateSample) [][]string {
	rows := make([][]string, 0, len(states))
	for _, state := range states {
		rows = append(rows, []string{
			state.RunID, state.TrialID, state.Timestamp, formatFloat(state.OffsetMS), state.FaultKind,
			strconv.FormatBool(state.KillSwitchEnabled), strconv.FormatInt(state.TransformInflight, 10),
			strconv.FormatInt(state.TransformWaiting, 10), strconv.Itoa(state.CoordinatorKeys),
			strconv.Itoa(state.CoordinatorWaiters), strconv.FormatInt(state.OriginalBytesInflight, 10),
			strconv.FormatInt(state.DerivativeHits, 10), strconv.FormatInt(state.DerivativeMisses, 10),
			strconv.FormatInt(state.TransformSuccess, 10), strconv.FormatInt(state.TransformError, 10),
			strconv.FormatInt(state.TransformTimeout, 10), strconv.FormatInt(state.TransformShed, 10),
			strconv.FormatInt(state.KillSwitchRejected, 10), strconv.FormatInt(state.FaultInjections, 10),
		})
	}
	return rows
}

func resourceRows(resources []ResourceSample) [][]string {
	rows := make([][]string, 0, len(resources))
	for _, sample := range resources {
		rows = append(rows, []string{
			sample.RunID, sample.TrialID, sample.Timestamp, formatFloat(sample.ElapsedMS),
			strconv.FormatInt(sample.CPUUsageUsec, 10), strconv.FormatInt(sample.RSSBytes, 10),
			strconv.FormatInt(sample.CgroupMemoryBytes, 10),
		})
	}
	return rows
}

// writeRunSummary rewrites the small summary files after every trial.
func writeRunSummary(output RunOutput, metrics []trialMetrics) error {
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
	return writeMetrics(filepath.Join(output.Directory, "metrics.prom"), metrics)
}

func trialRow(trial TrialResult) []string {
	hit := trial.Summaries[StreamHit]
	miss := trial.Summaries[StreamHealthyMiss]
	poisoned := trial.Summaries[StreamPoisonedMiss]
	return []string{
		trial.RunID, trial.TrialID, trial.Mode, trial.Fault, strconv.Itoa(trial.Repetition),
		strconv.FormatBool(trial.Valid), trial.InvalidReason, trial.StartedAt, trial.FinishedAt,
		strconv.Itoa(trial.TotalRequests), strconv.Itoa(trial.SuccessRequests), strconv.Itoa(trial.ErrorRequests),
		strconv.Itoa(trial.ShedRequests), strconv.Itoa(trial.KillSwitchRequests), strconv.Itoa(trial.SaturatedRequests),
		formatFloat(trial.FaultOnOffsetMS), formatFloat(trial.FaultOffOffsetMS),
		formatFloat(trial.KillSwitchOnOffsetMS), formatFloat(trial.KillSwitchOffOffsetMS),
		strconv.FormatInt(trial.DerivativeHits, 10), strconv.FormatInt(trial.DerivativeMisses, 10),
		strconv.FormatInt(trial.TransformSuccess, 10), strconv.FormatInt(trial.TransformError, 10),
		strconv.FormatInt(trial.TransformTimeout, 10), strconv.FormatInt(trial.TransformShed, 10),
		strconv.FormatInt(trial.KillSwitchRejected, 10), strconv.FormatInt(trial.FaultInjections, 10),
		strconv.FormatInt(trial.MaxTransformWaiting, 10), strconv.FormatInt(trial.MaxOriginalBytesInflight, 10),
		formatFloat(trial.CPUTimeMS), strconv.FormatInt(trial.PeakRSSBytes, 10), strconv.FormatInt(trial.PeakCgroupMemBytes, 10),
		formatFloat(hit[PhaseNormal].P99MS), formatFloat(hit[PhaseFault].P99MS), formatFloat(hit[PhaseRecovery].P99MS),
		strconv.Itoa(hit[PhaseFault].Errors), strconv.Itoa(hit[PhaseFault].Requests),
		formatFloat(miss[PhaseNormal].P99MS), formatFloat(miss[PhaseFault].P99MS), formatFloat(miss[PhaseRecovery].P99MS),
		strconv.Itoa(miss[PhaseFault].Errors), strconv.Itoa(miss[PhaseFault].Requests),
		strconv.Itoa(poisoned[PhaseFault].Errors), strconv.Itoa(poisoned[PhaseFault].Requests),
	}
}

func writeTrialsCSV(path string, trials []TrialResult) error {
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	rows := [][]string{trialColumns}
	for _, trial := range trials {
		rows = append(rows, trialRow(trial))
	}
	if err := writer.WriteAll(rows); err != nil {
		return err
	}
	return writeFileAtomic(path, buffer.Bytes())
}

func writeMetrics(path string, metrics []trialMetrics) error {
	var buffer bytes.Buffer
	for _, trial := range metrics {
		labels := fmt.Sprintf(`trial_id=%q,mode=%q,fault=%q`, trial.TrialID, trial.Mode, trial.Fault)
		write := func(name, extra string, value int64) {
			all := labels
			if extra != "" {
				all += "," + extra
			}
			fmt.Fprintf(&buffer, "%s{%s} %d\n", name, all, value)
		}
		s := trial.Snapshot
		write("media_derivative_requests_total", `result="hit"`, s.DerivativeHits)
		write("media_derivative_requests_total", `result="miss"`, s.DerivativeMisses)
		write("media_original_reads_total", `result="success"`, s.OriginalSuccess)
		write("media_original_reads_total", `result="error"`, s.OriginalError)
		write("media_transform_attempts_total", `result="success"`, s.TransformSuccess)
		write("media_transform_attempts_total", `result="error"`, s.TransformError)
		write("media_transform_attempts_total", `result="timeout"`, s.TransformTimeout)
		write("media_requests_coalesced_total", "", s.Coalesced)
		write("media_derivative_publish_attempts_total", `result="created"`, s.PublishCreated)
		write("media_derivative_publish_attempts_total", `result="existing"`, s.PublishExisting)
		write("media_derivative_publish_attempts_total", `result="error"`, s.PublishError)
		write("media_transform_wait_seconds_count", "", s.TransformWaitCount)
		write("media_transform_shed_total", "", s.TransformShed)
		write("media_kill_switch_rejected_total", "", s.KillSwitchRejected)
		write("media_fault_injections_total", `fault="slow-transform"`, s.FaultSlowTransform)
		write("media_fault_injections_total", `fault="transform-timeout"`, s.FaultTransformTimeout)
		write("media_fault_injections_total", `fault="transform-error"`, s.FaultTransformError)
		write("media_fault_injections_total", `fault="slow-original"`, s.FaultSlowOriginal)
		fmt.Fprintf(&buffer, "media_transform_duration_seconds_sum{%s} %.9f\n", labels, float64(s.TransformDurationNanos)/1e9)
		fmt.Fprintf(&buffer, "media_transform_wait_seconds_sum{%s} %.9f\n", labels, float64(s.TransformWaitNanos)/1e9)
		requestDurations := []struct {
			cache, result string
			count, nanos  int64
		}{
			{"miss", "success", s.RequestMissSuccessCount, s.RequestMissSuccessNanos},
			{"miss", "error", s.RequestMissErrorCount, s.RequestMissErrorNanos},
			{"derivative", "success", s.RequestHitSuccessCount, s.RequestHitSuccessNanos},
			{"derivative", "error", s.RequestHitErrorCount, s.RequestHitErrorNanos},
		}
		for _, value := range requestDurations {
			requestLabels := labels + fmt.Sprintf(`,cache=%q,result=%q`, value.cache, value.result)
			fmt.Fprintf(&buffer, "media_request_duration_seconds_sum{%s} %.9f\n", requestLabels, float64(value.nanos)/1e9)
			fmt.Fprintf(&buffer, "media_request_duration_seconds_count{%s} %d\n", requestLabels, value.count)
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
	return strconv.FormatFloat(value, 'f', 3, 64)
}
