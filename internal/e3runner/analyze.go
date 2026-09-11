package e3runner

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/parantail/content-serving-lab/internal/media"
)

// AnalysisOutput is the in-memory result of Analyze. Every table is also
// written as CSV under <run>/analysis so charts can be rebuilt from files.
type AnalysisOutput struct {
	Directory     string
	RunID         string
	Calibration   bool
	Metadata      RunMetadata
	TotalTrials   int
	ValidTrials   int
	InvalidTrials int
	Thresholds    map[string]float64
	BlastRadius   []BlastRow
	Resources     []ResourceRow
	NormalCost    []NormalCostRow
	Intervals     []IntervalRow
	Timeline      []TimelineRow
	KillSwitch    map[string]killSwitchOffsets
}

type BlastRow struct {
	Mode             string
	Fault            string
	Stream           string
	Phase            string
	Trials           int
	RequestsMean     float64
	SuccessRateMean  float64
	ErrorRateMean    float64
	ShedRateMean     float64
	KillRateMean     float64
	AffectedRateMean float64
	AffectedRateMin  float64
	AffectedRateMax  float64
	P50MeanMS        float64
	P95MeanMS        float64
	P99MeanMS        float64
	P99MinMS         float64
	P99MaxMS         float64
	MaxMeanMS        float64
}

type ResourceRow struct {
	Mode                     string
	Fault                    string
	Trials                   int
	PeakCgroupMemMeanBytes   float64
	PeakCgroupMemMaxBytes    int64
	MaxOriginalBytesMean     float64
	MaxOriginalBytesMax      int64
	MaxWaitingMean           float64
	MaxWaitingMax            int64
	CPUTimeMeanMS            float64
	PoisonedFaultErrorRate   float64
	PoisonedFaultP99MeanMS   float64
	TransformTimeoutMean     float64
	TransformShedMean        float64
	KillSwitchRejectedMean   float64
	FaultInjectionsMean      float64
	HealthyMissFaultHTTP500M float64
}

type NormalCostRow struct {
	Mode           string
	Stream         string
	Trials         int
	P50MeanMS      float64
	P95MeanMS      float64
	P99MeanMS      float64
	ErrorRateMean  float64
	ShedRateMean   float64
	P99DeltaVsBase float64
}

type IntervalRow struct {
	Mode                 string
	Fault                string
	Trials               int
	FirstImpactMeanMS    float64
	FirstImpactMinMS     float64
	FirstImpactMaxMS     float64
	TrialsWithImpact     int
	MitigationMeanMS     float64
	MitigationMinMS      float64
	MitigationMaxMS      float64
	TrialsWithMitigation int
	LastImpactAfterOffMS float64
	LastImpactAfterOffMx float64
	AffectedFaultMean    float64
	AffectedRecoveryMean float64
}

type TimelineRow struct {
	Mode             string
	Fault            string
	Stream           string
	BucketStartMS    float64
	Trials           int
	RequestsMean     float64
	ErrorRateMean    float64
	ShedRateMean     float64
	AffectedRateMean float64
	P99MeanMS        float64
	MaxMeanMS        float64
}

type killSwitchOffsets struct {
	OnMS  float64
	OffMS float64
	Count int
}

type analysisManifest struct {
	SchemaVersion     string             `json:"schema_version"`
	RunID             string             `json:"run_id"`
	Calibration       bool               `json:"calibration"`
	RawValidation     string             `json:"raw_validation"`
	TotalTrials       int                `json:"total_trials"`
	ValidTrials       int                `json:"valid_trials"`
	InvalidTrials     int                `json:"invalid_trials"`
	InvalidTrialsList map[string]string  `json:"invalid_trials_list"`
	Thresholds        map[string]float64 `json:"affected_latency_threshold_ms"`
	ThresholdRule     string             `json:"threshold_rule"`
	AggregationNote   string             `json:"aggregation_note"`
	Files             []string           `json:"files"`
}

type trialRaw struct {
	trial    TrialResult
	requests []RequestResult
	events   []Event
	metrics  map[string]int64
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
	requests, err := readRequests(filepath.Join(runDirectory, requestsFile))
	if err != nil {
		return AnalysisOutput{}, err
	}
	events, err := readEvents(filepath.Join(runDirectory, "logs.jsonl"))
	if err != nil {
		return AnalysisOutput{}, err
	}
	metrics, err := readMetrics(filepath.Join(runDirectory, "metrics.prom"))
	if err != nil {
		return AnalysisOutput{}, err
	}
	if len(trials) == 0 {
		return AnalysisOutput{}, errors.New("trials.csv has no trials")
	}

	raws := make(map[string]*trialRaw, len(trials))
	order := make([]string, 0, len(trials))
	for _, trial := range trials {
		if _, exists := raws[trial.TrialID]; exists {
			return AnalysisOutput{}, fmt.Errorf("duplicate trial %s in trials.csv", trial.TrialID)
		}
		raws[trial.TrialID] = &trialRaw{trial: trial, metrics: metrics[trial.TrialID]}
		order = append(order, trial.TrialID)
	}
	for _, request := range requests {
		raw, ok := raws[request.TrialID]
		if !ok {
			return AnalysisOutput{}, fmt.Errorf("requests.csv references unknown trial %s", request.TrialID)
		}
		raw.requests = append(raw.requests, request)
	}
	for _, event := range events {
		if raw, ok := raws[event.TrialID]; ok {
			raw.events = append(raw.events, event)
		}
	}

	config := configFromMetadata(metadata)
	invalidReasons := make(map[string]string)
	for _, id := range order {
		raw := raws[id]
		raw.trial.Summaries = summarizeStreams(raw.requests)
		revalidate(config, raw)
		if !raw.trial.Valid {
			invalidReasons[id] = raw.trial.InvalidReason
		}
	}

	output := AnalysisOutput{
		Directory:   filepath.Join(runDirectory, "analysis"),
		RunID:       metadata.RunID,
		Calibration: metadata.Calibration,
		Metadata:    metadata,
		TotalTrials: len(trials),
		KillSwitch:  make(map[string]killSwitchOffsets),
	}
	var valid []*trialRaw
	for _, id := range order {
		if raws[id].trial.Valid {
			valid = append(valid, raws[id])
			output.ValidTrials++
		} else {
			output.InvalidTrials++
		}
	}
	output.Thresholds = affectedThresholds(valid)
	output.BlastRadius = blastRadius(valid, output.Thresholds, metadata)
	output.Resources = resourceSummary(valid, metadata)
	output.NormalCost = normalCost(valid, metadata)
	output.Intervals = intervals(valid, output.Thresholds, metadata)
	output.Timeline = timeline(valid, output.Thresholds, metadata)
	for _, raw := range valid {
		if raw.trial.Mode == string(media.IsolationKillSwitch) && raw.trial.KillSwitchOnOffsetMS >= 0 {
			offsets := output.KillSwitch[raw.trial.Fault]
			offsets.OnMS += raw.trial.KillSwitchOnOffsetMS
			offsets.OffMS += raw.trial.KillSwitchOffOffsetMS
			offsets.Count++
			output.KillSwitch[raw.trial.Fault] = offsets
		}
	}
	for fault, offsets := range output.KillSwitch {
		if offsets.Count > 0 {
			offsets.OnMS /= float64(offsets.Count)
			offsets.OffMS /= float64(offsets.Count)
			output.KillSwitch[fault] = offsets
		}
	}

	if err := os.MkdirAll(output.Directory, 0o755); err != nil {
		return AnalysisOutput{}, err
	}
	files := []string{"analysis.json", "blast-radius.csv", "resources.csv", "normal-cost.csv", "intervals.csv", "timeline-summary.csv"}
	if err := writeCSV(filepath.Join(output.Directory, "blast-radius.csv"), blastRadiusRows(output.BlastRadius)); err != nil {
		return AnalysisOutput{}, err
	}
	if err := writeCSV(filepath.Join(output.Directory, "resources.csv"), resourceSummaryRows(output.Resources)); err != nil {
		return AnalysisOutput{}, err
	}
	if err := writeCSV(filepath.Join(output.Directory, "normal-cost.csv"), normalCostRows(output.NormalCost)); err != nil {
		return AnalysisOutput{}, err
	}
	if err := writeCSV(filepath.Join(output.Directory, "intervals.csv"), intervalRows(output.Intervals)); err != nil {
		return AnalysisOutput{}, err
	}
	if err := writeCSV(filepath.Join(output.Directory, "timeline-summary.csv"), timelineRows(output.Timeline)); err != nil {
		return AnalysisOutput{}, err
	}
	for _, fault := range metadata.Faults {
		name := "timeline-" + fault + ".svg"
		if err := writeFileAtomic(filepath.Join(output.Directory, name), timelineSVG(output, fault)); err != nil {
			return AnalysisOutput{}, err
		}
		files = append(files, name)
	}
	manifest := analysisManifest{
		SchemaVersion:     SchemaVersion,
		RunID:             output.RunID,
		Calibration:       output.Calibration,
		RawValidation:     "passed",
		TotalTrials:       output.TotalTrials,
		ValidTrials:       output.ValidTrials,
		InvalidTrials:     output.InvalidTrials,
		InvalidTrialsList: invalidReasons,
		Thresholds:        output.Thresholds,
		ThresholdRule:     "3 x the largest normal-phase p99 across all valid trials, per stream; a healthy request is affected when it fails or exceeds this latency",
		AggregationNote:   "Rows aggregate valid trials only. Rates and percentiles are computed per trial and then averaged, so trial boundaries are preserved. Timeline buckets are keyed by request start offset.",
		Files:             files,
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return AnalysisOutput{}, err
	}
	if err := writeFileAtomic(filepath.Join(output.Directory, "analysis.json"), append(data, '\n')); err != nil {
		return AnalysisOutput{}, err
	}
	return output, nil
}

func configFromMetadata(metadata RunMetadata) Config {
	return Config{
		RunID:              metadata.RunID,
		HitRate:            metadata.HitRate,
		HealthyMissRate:    metadata.HealthyMissRate,
		PoisonedMissRate:   metadata.PoisonedMissRate,
		NormalDuration:     durationFromMS(metadata.NormalDurationMS),
		FaultDuration:      durationFromMS(metadata.FaultDurationMS),
		RecoveryDuration:   durationFromMS(metadata.RecoveryDurationMS),
		KillSwitchOnDelay:  durationFromMS(metadata.KillSwitchOnDelayMS),
		KillSwitchOffDelay: durationFromMS(metadata.KillSwitchOffDelayMS),
		TimelineBucket:     durationFromMS(metadata.TimelineBucketMS),
	}
}

// revalidate recomputes the trial validity from raw rows, metrics and
// events, then adds analyzer-specific consistency reasons.
func revalidate(config Config, raw *trialRaw) {
	recorded := raw.trial
	item := scheduledTrial{ID: recorded.TrialID, Mode: media.IsolationMode(recorded.Mode), Fault: media.FaultKind(recorded.Fault), Repetition: recorded.Repetition}
	recomputed := TrialResult{Valid: true, FaultOnOffsetMS: -1, FaultOffOffsetMS: -1, KillSwitchOnOffsetMS: -1, KillSwitchOffOffsetMS: -1, Summaries: raw.trial.Summaries}
	for _, row := range raw.requests {
		if row.Stream == StreamPrewarm {
			continue
		}
		recomputed.TotalRequests++
		switch {
		case row.ErrorType == "generator_saturated":
			recomputed.SaturatedRequests++
		case row.HTTPStatus == http.StatusOK && row.ErrorType == "":
			recomputed.SuccessRequests++
		default:
			recomputed.ErrorRequests++
		}
		switch row.Isolation {
		case media.IsolationOutcomeShed:
			recomputed.ShedRequests++
		case media.IsolationOutcomeKillSwitch:
			recomputed.KillSwitchRequests++
		}
	}
	for _, event := range raw.events {
		switch event.Event {
		case "fault_on":
			recomputed.FaultOnOffsetMS = event.OffsetMS
		case "fault_off":
			recomputed.FaultOffOffsetMS = event.OffsetMS
		case "kill_switch_on":
			recomputed.KillSwitchOnOffsetMS = event.OffsetMS
		case "kill_switch_off":
			recomputed.KillSwitchOffOffsetMS = event.OffsetMS
		}
	}
	trial := &raw.trial
	if recomputed.TotalRequests != recorded.TotalRequests || recomputed.SuccessRequests != recorded.SuccessRequests ||
		recomputed.ErrorRequests != recorded.ErrorRequests || recomputed.ShedRequests != recorded.ShedRequests ||
		recomputed.KillSwitchRequests != recorded.KillSwitchRequests || recomputed.SaturatedRequests != recorded.SaturatedRequests {
		invalidate(trial, "analyzer:request_counts_mismatch")
	}
	for name, pair := range map[string][2]float64{
		"fault_on":        {recomputed.FaultOnOffsetMS, recorded.FaultOnOffsetMS},
		"fault_off":       {recomputed.FaultOffOffsetMS, recorded.FaultOffOffsetMS},
		"kill_switch_on":  {recomputed.KillSwitchOnOffsetMS, recorded.KillSwitchOnOffsetMS},
		"kill_switch_off": {recomputed.KillSwitchOffOffsetMS, recorded.KillSwitchOffOffsetMS},
	} {
		if abs(pair[0]-pair[1]) > 1 {
			invalidate(trial, "analyzer:event_offset_mismatch_"+name)
		}
	}
	if raw.metrics == nil {
		invalidate(trial, "analyzer:metrics_missing")
	} else {
		hits := 0
		misses := 0
		for _, row := range raw.requests {
			if row.Stream == StreamPrewarm || row.HTTPStatus == 0 {
				continue
			}
			if row.Cache == "derivative" {
				hits++
			} else {
				misses++
			}
		}
		if raw.metrics["shed"] != int64(recomputed.ShedRequests) {
			invalidate(trial, "analyzer:shed_metric_mismatch")
		}
		if raw.metrics["kill_switch_rejected"] != int64(recomputed.KillSwitchRequests) {
			invalidate(trial, "analyzer:kill_switch_metric_mismatch")
		}
		if raw.metrics["hits"] != int64(hits) {
			invalidate(trial, "analyzer:hit_metric_mismatch")
		}
		if raw.metrics["misses"] != int64(misses) {
			invalidate(trial, "analyzer:miss_metric_mismatch")
		}
		if raw.metrics["fault_injections"] != recorded.FaultInjections {
			invalidate(trial, "analyzer:fault_metric_mismatch")
		}
	}
	// Re-run the runner-side checks on the raw rows as well.
	check := recorded
	check.Valid = true
	check.InvalidReason = ""
	check.Summaries = raw.trial.Summaries
	check.SaturatedRequests = recomputed.SaturatedRequests
	validateTrial(config, item, &check, raw.requests, config.NormalDuration+config.FaultDuration+config.RecoveryDuration)
	if !check.Valid {
		for _, reason := range strings.Split(check.InvalidReason, ";") {
			invalidate(trial, reason)
		}
	}
}

func affectedThresholds(valid []*trialRaw) map[string]float64 {
	thresholds := make(map[string]float64)
	for _, stream := range []string{StreamHit, StreamHealthyMiss} {
		var values []float64
		for _, raw := range valid {
			if summary, ok := raw.trial.Summaries[stream][PhaseNormal]; ok && summary.Requests > 0 {
				values = append(values, summary.P99MS)
			}
		}
		if len(values) == 0 {
			thresholds[stream] = 0
			continue
		}
		// The normal-phase p99 of the hit stream swings by an order of
		// magnitude with CPU contention from concurrent transforms, so the
		// threshold uses the largest normal-phase p99 rather than the median.
		largest := 0.0
		for _, value := range values {
			largest = math.Max(largest, value)
		}
		thresholds[stream] = 3 * largest
	}
	return thresholds
}

func isAffected(row RequestResult, thresholds map[string]float64) bool {
	if row.ErrorType == "generator_saturated" {
		return false
	}
	if row.HTTPStatus != http.StatusOK || row.ErrorType != "" {
		return true
	}
	threshold := thresholds[row.Stream]
	return threshold > 0 && row.LatencyMS > threshold
}

type comboKey struct{ mode, fault string }

func orderedCombos(metadata RunMetadata) []comboKey {
	var combos []comboKey
	for _, fault := range metadata.Faults {
		for _, mode := range metadata.Modes {
			combos = append(combos, comboKey{mode: mode, fault: fault})
		}
	}
	return combos
}

func trialsFor(valid []*trialRaw, combo comboKey) []*trialRaw {
	var matches []*trialRaw
	for _, raw := range valid {
		if raw.trial.Mode == combo.mode && raw.trial.Fault == combo.fault {
			matches = append(matches, raw)
		}
	}
	return matches
}

func blastRadius(valid []*trialRaw, thresholds map[string]float64, metadata RunMetadata) []BlastRow {
	var rows []BlastRow
	for _, combo := range orderedCombos(metadata) {
		matches := trialsFor(valid, combo)
		for _, stream := range []string{StreamHit, StreamHealthyMiss, StreamPoisonedMiss} {
			for _, phase := range []string{PhaseNormal, PhaseFault, PhaseRecovery} {
				row := BlastRow{Mode: combo.mode, Fault: combo.fault, Stream: stream, Phase: phase, AffectedRateMin: math.Inf(1), P99MinMS: math.Inf(1)}
				for _, raw := range matches {
					summary, ok := raw.trial.Summaries[stream][phase]
					if !ok || summary.Requests == 0 {
						continue
					}
					affected := 0
					for _, request := range raw.requests {
						if request.Stream == stream && request.Phase == phase && isAffected(request, thresholds) {
							affected++
						}
					}
					served := float64(summary.Requests - summary.Saturated)
					if served == 0 {
						continue
					}
					row.Trials++
					row.RequestsMean += served
					row.SuccessRateMean += float64(summary.Success) / served
					row.ErrorRateMean += float64(summary.Errors) / served
					row.ShedRateMean += float64(summary.Shed) / served
					row.KillRateMean += float64(summary.KillSwitch) / served
					rate := float64(affected) / served
					row.AffectedRateMean += rate
					row.AffectedRateMin = math.Min(row.AffectedRateMin, rate)
					row.AffectedRateMax = math.Max(row.AffectedRateMax, rate)
					row.P50MeanMS += summary.P50MS
					row.P95MeanMS += summary.P95MS
					row.P99MeanMS += summary.P99MS
					row.P99MinMS = math.Min(row.P99MinMS, summary.P99MS)
					row.P99MaxMS = math.Max(row.P99MaxMS, summary.P99MS)
					row.MaxMeanMS += summary.MaxMS
				}
				if row.Trials == 0 {
					row.AffectedRateMin, row.P99MinMS = 0, 0
					rows = append(rows, row)
					continue
				}
				n := float64(row.Trials)
				row.RequestsMean /= n
				row.SuccessRateMean /= n
				row.ErrorRateMean /= n
				row.ShedRateMean /= n
				row.KillRateMean /= n
				row.AffectedRateMean /= n
				row.P50MeanMS /= n
				row.P95MeanMS /= n
				row.P99MeanMS /= n
				row.MaxMeanMS /= n
				rows = append(rows, row)
			}
		}
	}
	return rows
}

func resourceSummary(valid []*trialRaw, metadata RunMetadata) []ResourceRow {
	var rows []ResourceRow
	for _, combo := range orderedCombos(metadata) {
		matches := trialsFor(valid, combo)
		row := ResourceRow{Mode: combo.mode, Fault: combo.fault, Trials: len(matches)}
		for _, raw := range matches {
			trial := raw.trial
			row.PeakCgroupMemMeanBytes += float64(trial.PeakCgroupMemBytes)
			row.PeakCgroupMemMaxBytes = max(row.PeakCgroupMemMaxBytes, trial.PeakCgroupMemBytes)
			row.MaxOriginalBytesMean += float64(trial.MaxOriginalBytesInflight)
			row.MaxOriginalBytesMax = max(row.MaxOriginalBytesMax, trial.MaxOriginalBytesInflight)
			row.MaxWaitingMean += float64(trial.MaxTransformWaiting)
			row.MaxWaitingMax = max(row.MaxWaitingMax, trial.MaxTransformWaiting)
			row.CPUTimeMeanMS += trial.CPUTimeMS
			row.TransformTimeoutMean += float64(trial.TransformTimeout)
			row.TransformShedMean += float64(trial.TransformShed)
			row.KillSwitchRejectedMean += float64(trial.KillSwitchRejected)
			row.FaultInjectionsMean += float64(trial.FaultInjections)
			if poisoned, ok := trial.Summaries[StreamPoisonedMiss][PhaseFault]; ok && poisoned.Requests-poisoned.Saturated > 0 {
				row.PoisonedFaultErrorRate += float64(poisoned.Errors) / float64(poisoned.Requests-poisoned.Saturated)
				row.PoisonedFaultP99MeanMS += poisoned.P99MS
			}
			if miss, ok := trial.Summaries[StreamHealthyMiss][PhaseFault]; ok {
				row.HealthyMissFaultHTTP500M += float64(miss.HTTP500)
			}
		}
		if n := float64(len(matches)); n > 0 {
			row.PeakCgroupMemMeanBytes /= n
			row.MaxOriginalBytesMean /= n
			row.MaxWaitingMean /= n
			row.CPUTimeMeanMS /= n
			row.TransformTimeoutMean /= n
			row.TransformShedMean /= n
			row.KillSwitchRejectedMean /= n
			row.FaultInjectionsMean /= n
			row.PoisonedFaultErrorRate /= n
			row.PoisonedFaultP99MeanMS /= n
			row.HealthyMissFaultHTTP500M /= n
		}
		rows = append(rows, row)
	}
	return rows
}

func normalCost(valid []*trialRaw, metadata RunMetadata) []NormalCostRow {
	var rows []NormalCostRow
	baseline := make(map[string]float64)
	for _, mode := range metadata.Modes {
		matches := trialsFor(valid, comboKey{mode: mode, fault: string(media.FaultNone)})
		for _, stream := range []string{StreamHit, StreamHealthyMiss} {
			row := NormalCostRow{Mode: mode, Stream: stream}
			for _, raw := range matches {
				summary, ok := raw.trial.Summaries[stream]["all"]
				if !ok || summary.Requests-summary.Saturated == 0 {
					continue
				}
				served := float64(summary.Requests - summary.Saturated)
				row.Trials++
				row.P50MeanMS += summary.P50MS
				row.P95MeanMS += summary.P95MS
				row.P99MeanMS += summary.P99MS
				row.ErrorRateMean += float64(summary.Errors) / served
				row.ShedRateMean += float64(summary.Shed) / served
			}
			if n := float64(row.Trials); n > 0 {
				row.P50MeanMS /= n
				row.P95MeanMS /= n
				row.P99MeanMS /= n
				row.ErrorRateMean /= n
				row.ShedRateMean /= n
			}
			if mode == string(media.IsolationBaseline) {
				baseline[stream] = row.P99MeanMS
			}
			rows = append(rows, row)
		}
	}
	for index := range rows {
		if base, ok := baseline[rows[index].Stream]; ok && rows[index].Trials > 0 {
			rows[index].P99DeltaVsBase = rows[index].P99MeanMS - base
		}
	}
	return rows
}

func intervals(valid []*trialRaw, thresholds map[string]float64, metadata RunMetadata) []IntervalRow {
	var rows []IntervalRow
	for _, combo := range orderedCombos(metadata) {
		if combo.fault == string(media.FaultNone) {
			continue
		}
		matches := trialsFor(valid, combo)
		row := IntervalRow{Mode: combo.mode, Fault: combo.fault, Trials: len(matches), FirstImpactMinMS: math.Inf(1), MitigationMinMS: math.Inf(1)}
		for _, raw := range matches {
			trial := raw.trial
			faultOn, faultOff := trial.FaultOnOffsetMS, trial.FaultOffOffsetMS
			firstImpact := -1.0
			lastAfterOff := 0.0
			affectedFault, affectedRecovery := 0, 0
			firstShed := -1.0
			for _, request := range raw.requests {
				if request.Stream == StreamPrewarm || request.Stream == StreamPoisonedMiss {
					if request.Isolation == media.IsolationOutcomeShed && (firstShed < 0 || request.OffsetMS < firstShed) {
						firstShed = request.OffsetMS
					}
					continue
				}
				if request.Isolation == media.IsolationOutcomeShed && (firstShed < 0 || request.OffsetMS < firstShed) {
					firstShed = request.OffsetMS
				}
				if !isAffected(request, thresholds) || request.OffsetMS < faultOn {
					continue
				}
				if firstImpact < 0 || request.OffsetMS < firstImpact {
					firstImpact = request.OffsetMS
				}
				if request.Phase == PhaseFault {
					affectedFault++
				}
				if request.Phase == PhaseRecovery {
					affectedRecovery++
				}
				if request.OffsetMS >= faultOff {
					lastAfterOff = math.Max(lastAfterOff, request.OffsetMS-faultOff)
				}
			}
			if firstImpact >= 0 {
				delta := firstImpact - faultOn
				row.TrialsWithImpact++
				row.FirstImpactMeanMS += delta
				row.FirstImpactMinMS = math.Min(row.FirstImpactMinMS, delta)
				row.FirstImpactMaxMS = math.Max(row.FirstImpactMaxMS, delta)
			}
			mitigation := -1.0
			switch combo.mode {
			case string(media.IsolationBoundedWait):
				if firstShed >= 0 {
					mitigation = firstShed - faultOn
				}
			case string(media.IsolationKillSwitch):
				if trial.KillSwitchOnOffsetMS >= 0 {
					mitigation = trial.KillSwitchOnOffsetMS - faultOn
				}
			}
			if mitigation >= 0 {
				row.TrialsWithMitigation++
				row.MitigationMeanMS += mitigation
				row.MitigationMinMS = math.Min(row.MitigationMinMS, mitigation)
				row.MitigationMaxMS = math.Max(row.MitigationMaxMS, mitigation)
			}
			row.LastImpactAfterOffMS += lastAfterOff
			row.LastImpactAfterOffMx = math.Max(row.LastImpactAfterOffMx, lastAfterOff)
			row.AffectedFaultMean += float64(affectedFault)
			row.AffectedRecoveryMean += float64(affectedRecovery)
		}
		if row.TrialsWithImpact > 0 {
			row.FirstImpactMeanMS /= float64(row.TrialsWithImpact)
		} else {
			row.FirstImpactMinMS, row.FirstImpactMaxMS = 0, 0
		}
		if row.TrialsWithMitigation > 0 {
			row.MitigationMeanMS /= float64(row.TrialsWithMitigation)
		} else {
			row.MitigationMinMS, row.MitigationMaxMS = 0, 0
		}
		if n := float64(len(matches)); n > 0 {
			row.LastImpactAfterOffMS /= n
			row.AffectedFaultMean /= n
			row.AffectedRecoveryMean /= n
		}
		rows = append(rows, row)
	}
	return rows
}

func timeline(valid []*trialRaw, thresholds map[string]float64, metadata RunMetadata) []TimelineRow {
	bucket := metadata.TimelineBucketMS
	total := metadata.NormalDurationMS + metadata.FaultDurationMS + metadata.RecoveryDurationMS
	buckets := int(math.Ceil(total / bucket))
	type acc struct {
		trials   int
		requests float64
		errors   float64
		shed     float64
		affected float64
		p99      float64
		maxMS    float64
	}
	var rows []TimelineRow
	for _, combo := range orderedCombos(metadata) {
		matches := trialsFor(valid, combo)
		for _, stream := range []string{StreamHit, StreamHealthyMiss, StreamPoisonedMiss} {
			accumulators := make([]acc, buckets)
			for _, raw := range matches {
				latencies := make([][]float64, buckets)
				counts := make([]acc, buckets)
				for _, request := range raw.requests {
					if request.Stream != stream || request.ErrorType == "generator_saturated" {
						continue
					}
					index := int(math.Floor(request.OffsetMS / bucket))
					if index < 0 || index >= buckets {
						continue
					}
					counts[index].requests++
					if request.HTTPStatus != http.StatusOK || request.ErrorType != "" {
						counts[index].errors++
					}
					if request.Isolation == media.IsolationOutcomeShed {
						counts[index].shed++
					}
					if isAffected(request, thresholds) {
						counts[index].affected++
					}
					latencies[index] = append(latencies[index], request.LatencyMS)
				}
				for index := range buckets {
					if counts[index].requests == 0 {
						continue
					}
					sort.Float64s(latencies[index])
					accumulators[index].trials++
					accumulators[index].requests += counts[index].requests
					accumulators[index].errors += counts[index].errors / counts[index].requests
					accumulators[index].shed += counts[index].shed / counts[index].requests
					accumulators[index].affected += counts[index].affected / counts[index].requests
					accumulators[index].p99 += percentile(latencies[index], 0.99)
					accumulators[index].maxMS += latencies[index][len(latencies[index])-1]
				}
			}
			for index, value := range accumulators {
				row := TimelineRow{Mode: combo.mode, Fault: combo.fault, Stream: stream, BucketStartMS: float64(index) * bucket, Trials: value.trials}
				if value.trials > 0 {
					n := float64(value.trials)
					row.RequestsMean = value.requests / n
					row.ErrorRateMean = value.errors / n
					row.ShedRateMean = value.shed / n
					row.AffectedRateMean = value.affected / n
					row.P99MeanMS = value.p99 / n
					row.MaxMeanMS = value.maxMS / n
				}
				rows = append(rows, row)
			}
		}
	}
	return rows
}

func durationFromMS(value float64) time.Duration {
	return time.Duration(value * float64(time.Millisecond))
}

func readRunMetadata(path string) (RunMetadata, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return RunMetadata{}, err
	}
	var metadata RunMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return RunMetadata{}, fmt.Errorf("parse run.json: %w", err)
	}
	if metadata.SchemaVersion != SchemaVersion {
		return RunMetadata{}, fmt.Errorf("unsupported schema %q", metadata.SchemaVersion)
	}
	return metadata, nil
}

func readCSVRecords(path string, want []string) ([]map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var source io.Reader = bufio.NewReaderSize(file, 1<<20)
	if strings.HasSuffix(path, ".gz") {
		unzipped, err := gzip.NewReader(source)
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", filepath.Base(path), err)
		}
		defer unzipped.Close()
		source = unzipped
	}
	reader := csv.NewReader(source)
	reader.ReuseRecord = false
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("read %s header: %w", filepath.Base(path), err)
	}
	if strings.Join(header, ",") != strings.Join(want, ",") {
		return nil, fmt.Errorf("%s has unexpected columns", filepath.Base(path))
	}
	var records []map[string]string
	for {
		record, err := reader.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("read %s: %w", filepath.Base(path), err)
		}
		row := make(map[string]string, len(header))
		for index, column := range header {
			row[column] = record[index]
		}
		records = append(records, row)
	}
	return records, nil
}

func readTrials(path string) ([]TrialResult, error) {
	records, err := readCSVRecords(path, trialColumns)
	if err != nil {
		return nil, err
	}
	trials := make([]TrialResult, 0, len(records))
	for _, record := range records {
		trials = append(trials, TrialResult{
			RunID:                    record["run_id"],
			TrialID:                  record["trial_id"],
			Mode:                     record["mode"],
			Fault:                    record["fault"],
			Repetition:               atoi(record["repetition"]),
			Valid:                    record["valid"] == "true",
			InvalidReason:            record["invalid_reason"],
			StartedAt:                record["started_at"],
			FinishedAt:               record["finished_at"],
			TotalRequests:            atoi(record["total_requests"]),
			SuccessRequests:          atoi(record["success_requests"]),
			ErrorRequests:            atoi(record["error_requests"]),
			ShedRequests:             atoi(record["shed_requests"]),
			KillSwitchRequests:       atoi(record["kill_switch_requests"]),
			SaturatedRequests:        atoi(record["saturated_requests"]),
			FaultOnOffsetMS:          atof(record["fault_on_offset_ms"]),
			FaultOffOffsetMS:         atof(record["fault_off_offset_ms"]),
			KillSwitchOnOffsetMS:     atof(record["kill_switch_on_offset_ms"]),
			KillSwitchOffOffsetMS:    atof(record["kill_switch_off_offset_ms"]),
			DerivativeHits:           atoi64(record["derivative_hits"]),
			DerivativeMisses:         atoi64(record["derivative_misses"]),
			TransformSuccess:         atoi64(record["transform_success"]),
			TransformError:           atoi64(record["transform_error"]),
			TransformTimeout:         atoi64(record["transform_timeout"]),
			TransformShed:            atoi64(record["transform_shed"]),
			KillSwitchRejected:       atoi64(record["kill_switch_rejected"]),
			FaultInjections:          atoi64(record["fault_injections"]),
			MaxTransformWaiting:      atoi64(record["max_transform_waiting"]),
			MaxOriginalBytesInflight: atoi64(record["max_original_bytes_inflight"]),
			CPUTimeMS:                atof(record["cpu_time_ms"]),
			PeakRSSBytes:             atoi64(record["peak_rss_bytes"]),
			PeakCgroupMemBytes:       atoi64(record["peak_cgroup_memory_bytes"]),
		})
	}
	return trials, nil
}

func readRequests(path string) ([]RequestResult, error) {
	records, err := readCSVRecords(path, requestColumns)
	if err != nil {
		return nil, err
	}
	requests := make([]RequestResult, 0, len(records))
	for _, record := range records {
		requests = append(requests, RequestResult{
			RunID:          record["run_id"],
			TrialID:        record["trial_id"],
			Mode:           record["mode"],
			Fault:          record["fault"],
			Repetition:     atoi(record["repetition"]),
			Stream:         record["stream"],
			RequestID:      record["request_id"],
			SourceHash:     record["source_hash"],
			Spec:           record["spec"],
			ImageKey:       record["image_key"],
			StartedAt:      record["started_at"],
			FinishedAt:     record["finished_at"],
			OffsetMS:       atof(record["offset_ms"]),
			Phase:          record["phase"],
			LatencyMS:      atof(record["latency_ms"]),
			HTTPStatus:     atoi(record["http_status"]),
			Cache:          record["cache"],
			Coalesced:      record["coalesced"] == "true",
			Isolation:      record["isolation"],
			RetryAfter:     record["retry_after"],
			ErrorType:      record["error_type"],
			ResponseSHA256: record["response_sha256"],
		})
	}
	return requests, nil
}

func readEvents(path string) ([]Event, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var events []Event
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var event Event
		if err := json.Unmarshal(line, &event); err != nil {
			return nil, fmt.Errorf("parse logs.jsonl: %w", err)
		}
		events = append(events, event)
	}
	return events, scanner.Err()
}

// readMetrics extracts the per-trial counters the analyzer cross-checks.
func readMetrics(path string) (map[string]map[string]int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	metrics := make(map[string]map[string]int64)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		name, rest, ok := strings.Cut(line, "{")
		if !ok {
			continue
		}
		labels, valueText, ok := strings.Cut(rest, "} ")
		if !ok {
			continue
		}
		trialID := labelValue(labels, "trial_id")
		if trialID == "" {
			continue
		}
		if metrics[trialID] == nil {
			metrics[trialID] = make(map[string]int64)
		}
		value, err := strconv.ParseInt(strings.TrimSpace(valueText), 10, 64)
		if err != nil {
			continue
		}
		switch {
		case name == "media_derivative_requests_total" && labelValue(labels, "result") == "hit":
			metrics[trialID]["hits"] += value
		case name == "media_derivative_requests_total" && labelValue(labels, "result") == "miss":
			metrics[trialID]["misses"] += value
		case name == "media_transform_shed_total":
			metrics[trialID]["shed"] += value
		case name == "media_kill_switch_rejected_total":
			metrics[trialID]["kill_switch_rejected"] += value
		case name == "media_fault_injections_total":
			metrics[trialID]["fault_injections"] += value
		}
	}
	return metrics, scanner.Err()
}

func labelValue(labels, name string) string {
	for _, part := range strings.Split(labels, ",") {
		key, value, ok := strings.Cut(part, "=")
		if ok && key == name {
			return strings.Trim(value, `"`)
		}
	}
	return ""
}

func atoi(value string) int {
	parsed, _ := strconv.Atoi(value)
	return parsed
}

func atoi64(value string) int64 {
	parsed, _ := strconv.ParseInt(value, 10, 64)
	return parsed
}

func atof(value string) float64 {
	parsed, _ := strconv.ParseFloat(value, 64)
	return parsed
}

func writeCSV(path string, rows [][]string) error {
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	if err := writer.WriteAll(rows); err != nil {
		return err
	}
	return writeFileAtomic(path, buffer.Bytes())
}

func blastRadiusRows(rows []BlastRow) [][]string {
	out := [][]string{{"mode", "fault", "stream", "phase", "trials", "requests_mean", "success_rate_mean", "error_rate_mean", "shed_rate_mean", "kill_switch_rate_mean", "affected_rate_mean", "affected_rate_min", "affected_rate_max", "p50_mean_ms", "p95_mean_ms", "p99_mean_ms", "p99_min_ms", "p99_max_ms", "max_mean_ms"}}
	for _, row := range rows {
		out = append(out, []string{row.Mode, row.Fault, row.Stream, row.Phase, strconv.Itoa(row.Trials), formatFloat(row.RequestsMean), formatRate(row.SuccessRateMean), formatRate(row.ErrorRateMean), formatRate(row.ShedRateMean), formatRate(row.KillRateMean), formatRate(row.AffectedRateMean), formatRate(row.AffectedRateMin), formatRate(row.AffectedRateMax), formatFloat(row.P50MeanMS), formatFloat(row.P95MeanMS), formatFloat(row.P99MeanMS), formatFloat(row.P99MinMS), formatFloat(row.P99MaxMS), formatFloat(row.MaxMeanMS)})
	}
	return out
}

func resourceSummaryRows(rows []ResourceRow) [][]string {
	out := [][]string{{"mode", "fault", "trials", "peak_cgroup_memory_mean_bytes", "peak_cgroup_memory_max_bytes", "max_original_bytes_inflight_mean", "max_original_bytes_inflight_max", "max_transform_waiting_mean", "max_transform_waiting_max", "cpu_time_mean_ms", "poisoned_fault_error_rate_mean", "poisoned_fault_p99_mean_ms", "transform_timeout_mean", "transform_shed_mean", "kill_switch_rejected_mean", "fault_injections_mean", "healthy_miss_fault_http500_mean"}}
	for _, row := range rows {
		out = append(out, []string{row.Mode, row.Fault, strconv.Itoa(row.Trials), formatFloat(row.PeakCgroupMemMeanBytes), strconv.FormatInt(row.PeakCgroupMemMaxBytes, 10), formatFloat(row.MaxOriginalBytesMean), strconv.FormatInt(row.MaxOriginalBytesMax, 10), formatFloat(row.MaxWaitingMean), strconv.FormatInt(row.MaxWaitingMax, 10), formatFloat(row.CPUTimeMeanMS), formatRate(row.PoisonedFaultErrorRate), formatFloat(row.PoisonedFaultP99MeanMS), formatFloat(row.TransformTimeoutMean), formatFloat(row.TransformShedMean), formatFloat(row.KillSwitchRejectedMean), formatFloat(row.FaultInjectionsMean), formatFloat(row.HealthyMissFaultHTTP500M)})
	}
	return out
}

func normalCostRows(rows []NormalCostRow) [][]string {
	out := [][]string{{"mode", "stream", "trials", "p50_mean_ms", "p95_mean_ms", "p99_mean_ms", "error_rate_mean", "shed_rate_mean", "p99_delta_vs_baseline_ms"}}
	for _, row := range rows {
		out = append(out, []string{row.Mode, row.Stream, strconv.Itoa(row.Trials), formatFloat(row.P50MeanMS), formatFloat(row.P95MeanMS), formatFloat(row.P99MeanMS), formatRate(row.ErrorRateMean), formatRate(row.ShedRateMean), formatFloat(row.P99DeltaVsBase)})
	}
	return out
}

func intervalRows(rows []IntervalRow) [][]string {
	out := [][]string{{"mode", "fault", "trials", "trials_with_impact", "first_impact_mean_ms", "first_impact_min_ms", "first_impact_max_ms", "trials_with_mitigation", "mitigation_mean_ms", "mitigation_min_ms", "mitigation_max_ms", "last_impact_after_fault_off_mean_ms", "last_impact_after_fault_off_max_ms", "affected_healthy_fault_mean", "affected_healthy_recovery_mean"}}
	for _, row := range rows {
		out = append(out, []string{row.Mode, row.Fault, strconv.Itoa(row.Trials), strconv.Itoa(row.TrialsWithImpact), formatFloat(row.FirstImpactMeanMS), formatFloat(row.FirstImpactMinMS), formatFloat(row.FirstImpactMaxMS), strconv.Itoa(row.TrialsWithMitigation), formatFloat(row.MitigationMeanMS), formatFloat(row.MitigationMinMS), formatFloat(row.MitigationMaxMS), formatFloat(row.LastImpactAfterOffMS), formatFloat(row.LastImpactAfterOffMx), formatFloat(row.AffectedFaultMean), formatFloat(row.AffectedRecoveryMean)})
	}
	return out
}

func timelineRows(rows []TimelineRow) [][]string {
	out := [][]string{{"mode", "fault", "stream", "bucket_start_ms", "trials", "requests_mean", "error_rate_mean", "shed_rate_mean", "affected_rate_mean", "p99_mean_ms", "max_mean_ms"}}
	for _, row := range rows {
		out = append(out, []string{row.Mode, row.Fault, row.Stream, formatFloat(row.BucketStartMS), strconv.Itoa(row.Trials), formatFloat(row.RequestsMean), formatRate(row.ErrorRateMean), formatRate(row.ShedRateMean), formatRate(row.AffectedRateMean), formatFloat(row.P99MeanMS), formatFloat(row.MaxMeanMS)})
	}
	return out
}

func formatRate(value float64) string {
	return strconv.FormatFloat(value, 'f', 6, 64)
}
