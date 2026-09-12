// Package readmefigures draws the summary charts embedded in the repository
// README from the committed analysis outputs of E1, E2 and E3. Every number
// on a chart is read from those files so the figures can be regenerated and
// checked against the retained evidence.
package readmefigures

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Sources lists the retained analysis files the README figures are drawn from.
type Sources struct {
	E1LocalRun     string // E1 Phase A retained run.json
	E1LocalSummary string // E1 Phase A analysis-retained/summary.csv
	E1AWSRun       string // E1 AWS S4 retained run.json
	E1AWSSummary   string // E1 AWS S4 analysis/summary.csv (one row per trial)
	E2Conditions   string // E2 figures/conditions.json (24 engine-pair conditions)
	E3Run          string // E3 retained run.json
	E3AnalysisDir  string // E3 analysis directory with blast-radius, intervals and timeline-summary CSVs
}

// DefaultSources points at the retained runs the README reports on.
func DefaultSources(repoRoot string) Sources {
	e1 := filepath.Join(repoRoot, "experiments", "e1-cache-stampede")
	e3 := filepath.Join(repoRoot, "experiments", "e3-failure-isolation", "results", "retained-e8c21674a86f5d4737b23a59e2071da7754f4ff2")
	return Sources{
		E1LocalRun:     filepath.Join(e1, "results", "retained-20260902-68135d3ad33f", "run.json"),
		E1LocalSummary: filepath.Join(e1, "results", "retained-20260902-68135d3ad33f", "analysis-retained", "summary.csv"),
		E1AWSRun:       filepath.Join(e1, "results-aws-s4", "retained-ad80a58dee07", "run.json"),
		E1AWSSummary:   filepath.Join(e1, "results-aws-s4", "retained-ad80a58dee07", "analysis", "summary.csv"),
		E2Conditions:   filepath.Join(repoRoot, "reports", "e2-transformer-ab", "aws-20260910-c2", "figures", "conditions.json"),
		E3Run:          filepath.Join(e3, "run.json"),
		E3AnalysisDir:  filepath.Join(e3, "analysis"),
	}
}

// E1Data is the duplicate-transform evidence from the single-process and AWS runs.
type E1Data struct {
	LocalRunID       string
	LocalRepetitions int
	LocalCPUQuota    string
	LocalMemoryBytes int64
	LocalDerivative  string          // e.g. "640×640 WebP"
	None             E1LocalScenario // S1-100: no coalescing
	Singleflight     E1LocalScenario // S2-100: process-local coalescing
	AWSRunID         string
	AWSRegion        string
	AWSRequests      int
	AWSRepetitions   int
	AWSTasks         []E1AWSScenario // ordered by task count
}

// E1LocalScenario summarises one 100-request cold-burst scenario.
type E1LocalScenario struct {
	Scenario        string
	Mode            string
	Concurrency     int
	ValidTrials     int
	TransformsMean  float64
	P99MeanMS       float64
	PeakMemoryBytes float64
}

// E1AWSScenario summarises one AWS task-count scenario over its valid trials.
type E1AWSScenario struct {
	Scenario       string
	Tasks          int
	ValidTrials    int
	TransformsMean float64
}

// E2Condition is one engine-pair condition from conditions.json.
type E2Condition struct {
	Format           string  `json:"format"`
	Operation        string  `json:"operation"`
	Concurrency      int     `json:"concurrency"`
	VipsThroughput   float64 `json:"vips_throughput"`
	MagickThroughput float64 `json:"magick_throughput"`
	ThroughputRatio  float64 `json:"throughput_ratio_vips_over_magick"`
	VipsRSSMiB       float64 `json:"vips_rss_mib"`
	MagickRSSMiB     float64 `json:"magick_rss_mib"`
	RSSRatio         float64 `json:"rss_ratio_vips_over_magick"`
}

// E2Data groups the throughput ratios by output format in the report's order.
type E2Data struct {
	Formats    []E2Format
	Conditions int
	// RSSHigherWithVips counts conditions where the libvips worker's peak RSS
	// was above ImageMagick's.
	RSSHigherWithVips int
}

// E2Format holds the six conditions (three geometries × two concurrencies) of one format.
type E2Format struct {
	Format string
	Ratios []float64 // sorted ascending
}

// E3Data is the transform-timeout fault evidence used by the README timeline.
type E3Data struct {
	RunID              string
	Repetitions        int
	CPUQuota           string
	MemoryBytes        int64
	TransformSlots     int
	TransformTimeoutMS float64
	SlotWaitLimitMS    float64
	NormalMS           float64
	FaultMS            float64
	RecoveryMS         float64
	BucketMS           float64
	Fault              string
	BaselineMiss       []E3Point // healthy-miss p99 per bucket, baseline mode
	BoundedMiss        []E3Point // healthy-miss p99 per bucket, bounded-wait mode
	BaselineHit        []E3Point // hit p99 per bucket, baseline mode
	BaselineFaultP99MS float64
	BaselineLastImpact float64 // last affected request start, ms after fault end
	BoundedFaultP99MS  float64
	BoundedErrorRate   float64
	BoundedLastImpact  float64
	HitMaxP99MS        float64 // largest bucket p99 of the plotted hit series
	HitFaultErrors     float64 // largest fault-phase hit error rate across all modes
}

// E3Point is one timeline bucket.
type E3Point struct {
	StartMS float64
	P99MS   float64
}

func loadE1(sources Sources) (E1Data, error) {
	var data E1Data
	var local struct {
		RunID       string `json:"run_id"`
		Repetitions int    `json:"repetitions"`
		CPUQuota    string `json:"cpu_quota"`
		MemoryBytes int64  `json:"memory_limit_bytes"`
		Width       int    `json:"derivative_width"`
		Height      int    `json:"derivative_height"`
		Format      string `json:"derivative_format"`
	}
	if err := readJSON(sources.E1LocalRun, &local); err != nil {
		return data, err
	}
	data.LocalRunID, data.LocalRepetitions, data.LocalCPUQuota, data.LocalMemoryBytes = local.RunID, local.Repetitions, local.CPUQuota, local.MemoryBytes
	data.LocalDerivative = fmt.Sprintf("%d×%d %s", local.Width, local.Height, strings.Replace(strings.ToUpper(local.Format), "WEBP", "WebP", 1))

	rows, err := readCSV(sources.E1LocalSummary)
	if err != nil {
		return data, err
	}
	found := 0
	for _, row := range rows {
		concurrency, err := strconv.Atoi(row["concurrency"])
		if err != nil {
			return data, fmt.Errorf("%s: concurrency %q: %w", sources.E1LocalSummary, row["concurrency"], err)
		}
		if concurrency != 100 || !strings.HasPrefix(row["scenario"], "S") {
			continue
		}
		scenario := E1LocalScenario{Scenario: row["scenario"], Mode: row["mode"], Concurrency: concurrency}
		if scenario.ValidTrials, err = strconv.Atoi(row["valid_trials"]); err != nil {
			return data, fmt.Errorf("%s: valid_trials: %w", sources.E1LocalSummary, err)
		}
		if scenario.TransformsMean, err = parseFloat(row, "transform_attempts_mean"); err != nil {
			return data, err
		}
		if scenario.P99MeanMS, err = parseFloat(row, "p99_mean_ms"); err != nil {
			return data, err
		}
		if scenario.PeakMemoryBytes, err = parseFloat(row, "peak_cgroup_memory_mean_bytes"); err != nil {
			return data, err
		}
		switch scenario.Mode {
		case "none":
			data.None = scenario
		case "process-singleflight":
			data.Singleflight = scenario
		default:
			continue
		}
		found++
	}
	if found != 2 || data.None.ValidTrials == 0 || data.Singleflight.ValidTrials == 0 {
		return data, fmt.Errorf("%s: expected valid none and process-singleflight rows at concurrency 100", sources.E1LocalSummary)
	}

	var aws struct {
		RunID       string `json:"run_id"`
		Region      string `json:"region"`
		Requests    int    `json:"requests_per_trial"`
		Repetitions int    `json:"repetitions"`
	}
	if err := readJSON(sources.E1AWSRun, &aws); err != nil {
		return data, err
	}
	data.AWSRunID, data.AWSRegion, data.AWSRequests, data.AWSRepetitions = aws.RunID, aws.Region, aws.Requests, aws.Repetitions

	rows, err = readCSV(sources.E1AWSSummary)
	if err != nil {
		return data, err
	}
	byScenario := map[string]*E1AWSScenario{}
	sums := map[string]float64{}
	for _, row := range rows {
		if row["valid"] != "true" {
			continue
		}
		tasks, err := strconv.Atoi(row["expected_tasks"])
		if err != nil {
			return data, fmt.Errorf("%s: expected_tasks: %w", sources.E1AWSSummary, err)
		}
		transforms, err := parseFloat(row, "transform_attempts")
		if err != nil {
			return data, err
		}
		scenario := byScenario[row["scenario"]]
		if scenario == nil {
			scenario = &E1AWSScenario{Scenario: row["scenario"], Tasks: tasks}
			byScenario[row["scenario"]] = scenario
		}
		if scenario.Tasks != tasks {
			return data, fmt.Errorf("%s: scenario %s mixes task counts", sources.E1AWSSummary, row["scenario"])
		}
		scenario.ValidTrials++
		sums[row["scenario"]] += transforms
	}
	for name, scenario := range byScenario {
		scenario.TransformsMean = sums[name] / float64(scenario.ValidTrials)
		data.AWSTasks = append(data.AWSTasks, *scenario)
	}
	sort.Slice(data.AWSTasks, func(i, j int) bool { return data.AWSTasks[i].Tasks < data.AWSTasks[j].Tasks })
	if len(data.AWSTasks) == 0 {
		return data, fmt.Errorf("%s: no valid trials", sources.E1AWSSummary)
	}
	return data, nil
}

var e2FormatOrder = []string{"jpeg", "png", "webp", "avif"}

func loadE2(sources Sources) (E2Data, error) {
	var conditions []E2Condition
	if err := readJSON(sources.E2Conditions, &conditions); err != nil {
		return E2Data{}, err
	}
	data := E2Data{Conditions: len(conditions)}
	byFormat := map[string][]float64{}
	for _, condition := range conditions {
		if condition.ThroughputRatio <= 0 {
			return data, fmt.Errorf("%s: non-positive throughput ratio for %s/%s/%d", sources.E2Conditions, condition.Format, condition.Operation, condition.Concurrency)
		}
		byFormat[condition.Format] = append(byFormat[condition.Format], condition.ThroughputRatio)
		if condition.RSSRatio > 1 {
			data.RSSHigherWithVips++
		}
	}
	for _, format := range e2FormatOrder {
		ratios := byFormat[format]
		if len(ratios) == 0 {
			return data, fmt.Errorf("%s: no conditions for format %s", sources.E2Conditions, format)
		}
		sort.Float64s(ratios)
		data.Formats = append(data.Formats, E2Format{Format: format, Ratios: ratios})
		delete(byFormat, format)
	}
	if len(byFormat) != 0 {
		return data, fmt.Errorf("%s: unexpected formats %v", sources.E2Conditions, byFormat)
	}
	return data, nil
}

const e3Fault = "transform-timeout"

func loadE3(sources Sources) (E3Data, error) {
	data := E3Data{Fault: e3Fault}
	var run struct {
		RunID              string  `json:"run_id"`
		Repetitions        int     `json:"repetitions"`
		CPUQuota           string  `json:"cpu_quota"`
		MemoryBytes        int64   `json:"memory_limit_bytes"`
		TransformSlots     int     `json:"transform_concurrency"`
		TransformTimeoutMS float64 `json:"transform_timeout_ms"`
		SlotWaitLimitMS    float64 `json:"slot_wait_limit_ms"`
		NormalMS           float64 `json:"normal_duration_ms"`
		FaultMS            float64 `json:"fault_duration_ms"`
		RecoveryMS         float64 `json:"recovery_duration_ms"`
		BucketMS           float64 `json:"timeline_bucket_ms"`
	}
	if err := readJSON(sources.E3Run, &run); err != nil {
		return data, err
	}
	data.RunID, data.Repetitions, data.CPUQuota, data.MemoryBytes = run.RunID, run.Repetitions, run.CPUQuota, run.MemoryBytes
	data.TransformSlots, data.TransformTimeoutMS, data.SlotWaitLimitMS = run.TransformSlots, run.TransformTimeoutMS, run.SlotWaitLimitMS
	data.NormalMS, data.FaultMS, data.RecoveryMS, data.BucketMS = run.NormalMS, run.FaultMS, run.RecoveryMS, run.BucketMS
	if data.BucketMS <= 0 || data.FaultMS <= 0 {
		return data, fmt.Errorf("%s: timeline_bucket_ms and fault_duration_ms must be positive", sources.E3Run)
	}

	timelinePath := filepath.Join(sources.E3AnalysisDir, "timeline-summary.csv")
	rows, err := readCSV(timelinePath)
	if err != nil {
		return data, err
	}
	series := map[string]*[]E3Point{
		"baseline/healthy-miss":     &data.BaselineMiss,
		"bounded-wait/healthy-miss": &data.BoundedMiss,
		"baseline/hit":              &data.BaselineHit,
	}
	for _, row := range rows {
		if row["fault"] != e3Fault {
			continue
		}
		target := series[row["mode"]+"/"+row["stream"]]
		if target == nil {
			continue
		}
		start, err := parseFloat(row, "bucket_start_ms")
		if err != nil {
			return data, err
		}
		p99, err := parseFloat(row, "p99_mean_ms")
		if err != nil {
			return data, err
		}
		*target = append(*target, E3Point{StartMS: start, P99MS: p99})
	}
	for name, points := range series {
		if len(*points) == 0 {
			return data, fmt.Errorf("%s: no %s buckets for fault %s", timelinePath, name, e3Fault)
		}
		sort.Slice(*points, func(i, j int) bool { return (*points)[i].StartMS < (*points)[j].StartMS })
	}
	for _, point := range data.BaselineHit {
		if point.P99MS > data.HitMaxP99MS {
			data.HitMaxP99MS = point.P99MS
		}
	}

	blastPath := filepath.Join(sources.E3AnalysisDir, "blast-radius.csv")
	rows, err = readCSV(blastPath)
	if err != nil {
		return data, err
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if row["fault"] != e3Fault || row["phase"] != "fault" {
			continue
		}
		p99, err := parseFloat(row, "p99_mean_ms")
		if err != nil {
			return data, err
		}
		errorRate, err := parseFloat(row, "error_rate_mean")
		if err != nil {
			return data, err
		}
		key := row["mode"] + "/" + row["stream"]
		seen[key] = true
		switch key {
		case "baseline/healthy-miss":
			data.BaselineFaultP99MS = p99
		case "bounded-wait/healthy-miss":
			data.BoundedFaultP99MS, data.BoundedErrorRate = p99, errorRate
		}
		if row["stream"] == "hit" && errorRate > data.HitFaultErrors {
			data.HitFaultErrors = errorRate
		}
	}
	for _, key := range []string{"baseline/healthy-miss", "bounded-wait/healthy-miss", "baseline/hit", "bounded-wait/hit"} {
		if !seen[key] {
			return data, fmt.Errorf("%s: missing fault-phase row %s", blastPath, key)
		}
	}

	intervalsPath := filepath.Join(sources.E3AnalysisDir, "intervals.csv")
	rows, err = readCSV(intervalsPath)
	if err != nil {
		return data, err
	}
	seen = map[string]bool{}
	for _, row := range rows {
		if row["fault"] != e3Fault {
			continue
		}
		lastImpact, err := parseFloat(row, "last_impact_after_fault_off_mean_ms")
		if err != nil {
			return data, err
		}
		seen[row["mode"]] = true
		switch row["mode"] {
		case "baseline":
			data.BaselineLastImpact = lastImpact
		case "bounded-wait":
			data.BoundedLastImpact = lastImpact
		}
	}
	if !seen["baseline"] || !seen["bounded-wait"] {
		return data, fmt.Errorf("%s: missing baseline or bounded-wait row for fault %s", intervalsPath, e3Fault)
	}
	return data, nil
}

func readJSON(path string, target any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func readCSV(path string) ([]map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := csv.NewReader(file)
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("%s: header: %w", path, err)
	}
	var rows []map[string]string
	for {
		record, err := reader.Read()
		if err == io.EOF {
			return rows, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		row := make(map[string]string, len(header))
		for i, name := range header {
			row[name] = record[i]
		}
		rows = append(rows, row)
	}
}

func parseFloat(row map[string]string, column string) (float64, error) {
	value, err := strconv.ParseFloat(row[column], 64)
	if err != nil {
		return 0, fmt.Errorf("column %s: %q: %w", column, row[column], err)
	}
	return value, nil
}
