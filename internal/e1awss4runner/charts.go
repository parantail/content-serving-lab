package e1awss4runner

import (
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
)

func writeSummary(path string, trials []TrialRecord) error {
	rows := [][]string{{
		"scenario", "trial_id", "repetition", "valid", "invalid_reason", "expected_tasks", "requests", "p50_ms", "p95_ms", "p99_ms",
		"transform_attempts", "original_get_count", "publish_created", "publish_existing", "publish_conflict", "cpu_time_ms", "peak_memory_bytes",
	}}
	for _, trial := range trials {
		rows = append(rows, []string{
			trial.Scenario, trial.TrialID, strconv.Itoa(trial.Repetition), strconv.FormatBool(trial.Valid), trial.InvalidReason,
			strconv.Itoa(trial.ExpectedTasks), strconv.Itoa(trial.TotalRequests), formatFloat(trial.P50MS), formatFloat(trial.P95MS),
			formatFloat(trial.P99MS), strconv.FormatInt(trial.TransformAttempts, 10), strconv.FormatInt(trial.OriginalGetCount, 10),
			strconv.FormatInt(trial.PublishCreated, 10), strconv.FormatInt(trial.PublishExisting, 10), strconv.FormatInt(trial.PublishConflict, 10),
			formatFloat(float64(trial.CPUUsageNanos) / 1e6), strconv.FormatUint(trial.PeakMemoryBytes, 10),
		})
	}
	return writeCSV(path, rows)
}

func taskDistributionSVG(trials []TrialRecord, tasks []TaskRecord) string {
	trialValid := make(map[string]bool, len(trials))
	for _, trial := range trials {
		trialValid[trial.TrialID] = trial.Valid
	}
	type row struct {
		label string
		share float64
	}
	var rows []row
	for _, task := range tasks {
		if !trialValid[task.TrialID] {
			continue
		}
		rows = append(rows, row{
			label: task.TrialID + " / " + shortTaskID(task.Task.TaskID),
			share: float64(task.Counters.ImageRequests) / RequestsPerTrial * 100,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].label < rows[j].label })
	height := 100 + len(rows)*24
	var body strings.Builder
	for index, row := range rows {
		y := 70 + index*24
		width := row.share * 5
		fmt.Fprintf(&body, `<text x="10" y="%d" font-size="11">%s</text><rect x="310" y="%d" width="%.2f" height="14" fill="#2f6f9f"/><text x="%.2f" y="%d" font-size="11">%.1f%%</text>`, y+11, html.EscapeString(row.label), y, width, 318+width, y+11, row.share)
	}
	return svgDocument(900, height, "Task request distribution — valid trials, 100 requests each", body.String())
}

func duplicateWorkSVG(trials []TrialRecord) string {
	type aggregate struct {
		trials     int
		transforms float64
		originals  float64
		puts       float64
	}
	values := make(map[string]*aggregate)
	for _, trial := range trials {
		if !trial.Valid {
			continue
		}
		value := values[trial.Scenario]
		if value == nil {
			value = &aggregate{}
			values[trial.Scenario] = value
		}
		value.trials++
		value.transforms += float64(trial.TransformAttempts)
		value.originals += float64(trial.OriginalGetCount)
		value.puts += float64(trial.PublishCreated + trial.PublishExisting + trial.PublishConflict + trial.PublishError)
	}
	var body strings.Builder
	for index, scenario := range []string{ScenarioTask1, ScenarioTask2, ScenarioTask4} {
		value := values[scenario]
		if value == nil || value.trials == 0 {
			continue
		}
		y := 80 + index*100
		fmt.Fprintf(&body, `<text x="10" y="%d" font-size="13">%s</text>`, y, scenario)
		metrics := []struct {
			name  string
			value float64
			color string
		}{{"transform", value.transforms / float64(value.trials), "#2f6f9f"}, {"original GET", value.originals / float64(value.trials), "#e07a5f"}, {"conditional PUT", value.puts / float64(value.trials), "#59a14f"}}
		for metricIndex, metric := range metrics {
			metricY := y + 10 + metricIndex*23
			fmt.Fprintf(&body, `<text x="180" y="%d" font-size="11">%s</text><rect x="280" y="%d" width="%.2f" height="14" fill="%s"/><text x="%.2f" y="%d" font-size="11">%.2f</text>`, metricY+11, metric.name, metricY, metric.value*90, metric.color, 287+metric.value*90, metricY+11, metric.value)
		}
	}
	return svgDocument(900, 390, "Duplicate work per trial — mean of valid trials", body.String())
}

func latencyCostSVG(trials []TrialRecord) string {
	type aggregate struct {
		trials int
		p99    float64
		cpuMS  float64
	}
	values := make(map[string]*aggregate)
	for _, trial := range trials {
		if !trial.Valid {
			continue
		}
		value := values[trial.Scenario]
		if value == nil {
			value = &aggregate{}
			values[trial.Scenario] = value
		}
		value.trials++
		value.p99 += trial.P99MS
		value.cpuMS += float64(trial.CPUUsageNanos) / 1e6
	}
	var body strings.Builder
	for index, scenario := range []string{ScenarioTask1, ScenarioTask2, ScenarioTask4} {
		value := values[scenario]
		if value == nil || value.trials == 0 {
			continue
		}
		y := 90 + index*90
		p99 := value.p99 / float64(value.trials)
		cpu := value.cpuMS / float64(value.trials)
		fmt.Fprintf(&body, `<text x="10" y="%d" font-size="13">%s</text><text x="220" y="%d" font-size="12">mean p99 %.3f ms</text><text x="480" y="%d" font-size="12">mean measured Task CPU %.3f ms</text>`, y, scenario, y, p99, y, cpu)
	}
	return svgDocument(900, 370, "Latency and measured Task CPU — valid trial means", body.String())
}

func svgDocument(width, height int, title, body string) string {
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d"><rect width="100%%" height="100%%" fill="#fff"/><text x="10" y="28" font-size="18" font-family="sans-serif">%s</text><g font-family="sans-serif">%s</g></svg>`+"\n", width, height, width, height, html.EscapeString(title), body)
}

func shortTaskID(taskID string) string {
	if len(taskID) <= 12 {
		return taskID
	}
	return taskID[:12]
}
