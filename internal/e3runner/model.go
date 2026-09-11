// Package e3runner drives the E3 failure isolation experiment: it embeds the
// media service with one isolation mode, applies one deterministic fault
// through the E3 controls, sends three open-loop request streams across a
// normal → fault → recovery timeline and records raw results that the
// analyzer cross-checks.
package e3runner

import (
	"runtime"
	"time"

	"github.com/parantail/content-serving-lab/internal/media"
)

const SchemaVersion = "e3-v1"

const (
	StreamHit          = "hit"
	StreamHealthyMiss  = "healthy-miss"
	StreamPoisonedMiss = "poisoned-miss"
	StreamPrewarm      = "prewarm"

	PhasePrewarm  = "prewarm"
	PhaseNormal   = "normal"
	PhaseFault    = "fault"
	PhaseRecovery = "recovery"
	PhaseDrain    = "drain"

	StorageLocal = "local"
	StorageS3    = "s3"
)

type Config struct {
	RunID          string
	ResultsRoot    string
	FixturePath    string
	GitCommit      string
	ContainerImage string
	Command        []string
	Calibration    bool

	Storage          string
	OriginalBucket   string
	DerivativeBucket string

	Modes       []media.IsolationMode
	Faults      []media.FaultKind
	Repetitions int

	TransformConcurrency int
	RequestTimeout       time.Duration
	TransformTimeout     time.Duration
	SlotWaitLimit        time.Duration
	FaultDelay           time.Duration
	KillSwitchOnDelay    time.Duration
	KillSwitchOffDelay   time.Duration

	NormalDuration   time.Duration
	FaultDuration    time.Duration
	RecoveryDuration time.Duration

	HitRate          float64
	HealthyMissRate  float64
	PoisonedMissRate float64
	HitKeys          int
	MaxInflight      int

	ResourceSampleGap time.Duration
	StateSampleGap    time.Duration
	TimelineBucket    time.Duration

	// transformer replaces the libvips transformer in tests.
	transformer media.Transformer
}

func (c Config) totalDuration() time.Duration {
	return c.NormalDuration + c.FaultDuration + c.RecoveryDuration
}

type RunMetadata struct {
	SchemaVersion        string            `json:"schema_version"`
	RunID                string            `json:"run_id"`
	CreatedAt            string            `json:"created_at"`
	Calibration          bool              `json:"calibration"`
	GitCommit            string            `json:"git_commit"`
	ContainerImage       string            `json:"container_image"`
	GoVersion            string            `json:"go_version"`
	OS                   string            `json:"os"`
	Architecture         string            `json:"architecture"`
	Hostname             string            `json:"hostname"`
	Transformer          string            `json:"transformer"`
	Storage              string            `json:"storage"`
	OriginalBucket       string            `json:"original_bucket,omitempty"`
	DerivativeBucket     string            `json:"derivative_bucket,omitempty"`
	FixturePath          string            `json:"fixture_path"`
	FixtureSHA256        string            `json:"fixture_sha256"`
	FixtureBytes         int               `json:"fixture_bytes"`
	SourceHash           string            `json:"source_hash"`
	PoisonedSourceHash   string            `json:"poisoned_source_hash"`
	HitSpecs             []string          `json:"hit_specs"`
	Modes                []string          `json:"modes"`
	Faults               []string          `json:"faults"`
	Repetitions          int               `json:"repetitions"`
	TransformConcurrency int               `json:"transform_concurrency"`
	RequestTimeoutMS     float64           `json:"request_timeout_ms"`
	TransformTimeoutMS   float64           `json:"transform_timeout_ms"`
	SlotWaitLimitMS      float64           `json:"slot_wait_limit_ms"`
	FaultDelayMS         float64           `json:"fault_delay_ms"`
	KillSwitchOnDelayMS  float64           `json:"kill_switch_on_delay_ms"`
	KillSwitchOffDelayMS float64           `json:"kill_switch_off_delay_ms"`
	NormalDurationMS     float64           `json:"normal_duration_ms"`
	FaultDurationMS      float64           `json:"fault_duration_ms"`
	RecoveryDurationMS   float64           `json:"recovery_duration_ms"`
	HitRate              float64           `json:"hit_rate_per_second"`
	HealthyMissRate      float64           `json:"healthy_miss_rate_per_second"`
	PoisonedMissRate     float64           `json:"poisoned_miss_rate_per_second"`
	MaxInflight          int               `json:"max_inflight_per_stream"`
	ResourceSampleGapMS  float64           `json:"resource_sample_gap_ms"`
	StateSampleGapMS     float64           `json:"state_sample_gap_ms"`
	TimelineBucketMS     float64           `json:"timeline_bucket_ms"`
	CPUQuota             string            `json:"cpu_quota"`
	MemoryLimitBytes     int64             `json:"memory_limit_bytes"`
	ExecutionOrder       []string          `json:"execution_order"`
	Commands             []string          `json:"commands"`
	Environment          map[string]string `json:"environment"`
	KnownLimitations     []string          `json:"known_limitations"`
}

func NewRunMetadata(config Config) RunMetadata {
	modes := make([]string, 0, len(config.Modes))
	for _, mode := range config.Modes {
		modes = append(modes, string(mode))
	}
	faults := make([]string, 0, len(config.Faults))
	for _, fault := range config.Faults {
		faults = append(faults, string(fault))
	}
	return RunMetadata{
		SchemaVersion:        SchemaVersion,
		RunID:                config.RunID,
		CreatedAt:            time.Now().UTC().Format(time.RFC3339Nano),
		Calibration:          config.Calibration,
		GitCommit:            config.GitCommit,
		ContainerImage:       config.ContainerImage,
		GoVersion:            runtime.Version(),
		OS:                   runtime.GOOS,
		Architecture:         runtime.GOARCH,
		Storage:              config.Storage,
		OriginalBucket:       config.OriginalBucket,
		DerivativeBucket:     config.DerivativeBucket,
		Modes:                modes,
		Faults:               faults,
		Repetitions:          config.Repetitions,
		TransformConcurrency: config.TransformConcurrency,
		RequestTimeoutMS:     milliseconds(config.RequestTimeout),
		TransformTimeoutMS:   milliseconds(config.TransformTimeout),
		SlotWaitLimitMS:      milliseconds(config.SlotWaitLimit),
		FaultDelayMS:         milliseconds(config.FaultDelay),
		KillSwitchOnDelayMS:  milliseconds(config.KillSwitchOnDelay),
		KillSwitchOffDelayMS: milliseconds(config.KillSwitchOffDelay),
		NormalDurationMS:     milliseconds(config.NormalDuration),
		FaultDurationMS:      milliseconds(config.FaultDuration),
		RecoveryDurationMS:   milliseconds(config.RecoveryDuration),
		HitRate:              config.HitRate,
		HealthyMissRate:      config.HealthyMissRate,
		PoisonedMissRate:     config.PoisonedMissRate,
		MaxInflight:          config.MaxInflight,
		ResourceSampleGapMS:  milliseconds(config.ResourceSampleGap),
		StateSampleGapMS:     milliseconds(config.StateSampleGap),
		TimelineBucketMS:     milliseconds(config.TimelineBucket),
		Environment:          make(map[string]string),
	}
}

// RequestResult is one row of requests.csv.
type RequestResult struct {
	RunID          string
	TrialID        string
	Mode           string
	Fault          string
	Repetition     int
	Stream         string
	RequestID      string
	SourceHash     string
	Spec           string
	ImageKey       string
	StartedAt      string
	FinishedAt     string
	OffsetMS       float64
	Phase          string
	LatencyMS      float64
	HTTPStatus     int
	Cache          string
	Coalesced      bool
	Isolation      string
	RetryAfter     string
	ErrorType      string
	ResponseSHA256 string
}

// Event is one row of logs.jsonl: fault and kill switch transitions plus
// phase boundaries, all with the trial-relative offset.
type Event struct {
	Timestamp string  `json:"timestamp"`
	RunID     string  `json:"run_id"`
	TrialID   string  `json:"trial_id"`
	OffsetMS  float64 `json:"offset_ms"`
	Event     string  `json:"event"`
	Detail    string  `json:"detail,omitempty"`
}

// StateSample is one row of state.csv: server-side gauges and cumulative
// counters observed through the E3 controller during a trial.
type StateSample struct {
	RunID                 string
	TrialID               string
	Timestamp             string
	OffsetMS              float64
	FaultKind             string
	KillSwitchEnabled     bool
	TransformInflight     int64
	TransformWaiting      int64
	CoordinatorKeys       int
	CoordinatorWaiters    int
	OriginalBytesInflight int64
	DerivativeHits        int64
	DerivativeMisses      int64
	TransformSuccess      int64
	TransformError        int64
	TransformTimeout      int64
	TransformShed         int64
	KillSwitchRejected    int64
	FaultInjections       int64
}

type ResourceSample struct {
	RunID             string
	TrialID           string
	Timestamp         string
	ElapsedMS         float64
	CPUUsageUsec      int64
	RSSBytes          int64
	CgroupMemoryBytes int64
}

// StreamPhaseSummary aggregates one stream within one phase of one trial.
type StreamPhaseSummary struct {
	Requests   int
	Success    int
	Errors     int
	Shed       int
	KillSwitch int
	HTTP500    int
	Timeouts   int
	Saturated  int
	P50MS      float64
	P95MS      float64
	P99MS      float64
	MaxMS      float64
	MeanMS     float64
}

// TrialResult is one row of trials.csv.
type TrialResult struct {
	RunID                    string
	TrialID                  string
	Mode                     string
	Fault                    string
	Repetition               int
	Valid                    bool
	InvalidReason            string
	StartedAt                string
	FinishedAt               string
	TotalRequests            int
	SuccessRequests          int
	ErrorRequests            int
	ShedRequests             int
	KillSwitchRequests       int
	SaturatedRequests        int
	FaultOnOffsetMS          float64
	FaultOffOffsetMS         float64
	KillSwitchOnOffsetMS     float64
	KillSwitchOffOffsetMS    float64
	DerivativeHits           int64
	DerivativeMisses         int64
	TransformSuccess         int64
	TransformError           int64
	TransformTimeout         int64
	TransformShed            int64
	KillSwitchRejected       int64
	FaultInjections          int64
	MaxTransformWaiting      int64
	MaxOriginalBytesInflight int64
	CPUTimeMS                float64
	PeakRSSBytes             int64
	PeakCgroupMemBytes       int64
	// Summaries are keyed by stream then phase.
	Summaries map[string]map[string]StreamPhaseSummary
}

func milliseconds(duration time.Duration) float64 {
	return float64(duration) / float64(time.Millisecond)
}
