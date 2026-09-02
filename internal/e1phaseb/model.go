package e1phaseb

import (
	"runtime"
	"time"

	"github.com/parantail/content-serving-lab/internal/media"
)

const SchemaVersion = "e1-phase-b-v1"

const (
	ScenarioS3ColdControl = "S3-COLD-CONTROL"
	ScenarioS3Cold        = "S3-COLD"
	ScenarioS3WarmControl = "S3-WARM-CONTROL"
	ScenarioS3Warm        = "S3-WARM"
	ScenarioF2            = "F2"
	ScenarioS42           = "S4-2"
	ScenarioS44           = "S4-4"
)

const (
	RequestClassHot       = "hot"
	RequestClassUnrelated = "unrelated"
	RequestClassFollowUp  = "follow-up"
)

const (
	RequestRoleBurst    = "burst"
	RequestRoleLeader   = "leader"
	RequestRoleWaiter   = "waiter"
	RequestRoleFollowUp = "follow-up"
)

type Config struct {
	RunID                string
	ResultsRoot          string
	FixturePath          string
	GitCommit            string
	ContainerImage       string
	Command              []string
	Calibration          bool
	Repetitions          int
	TransformConcurrency int
	RequestTimeout       time.Duration
	TransformTimeout     time.Duration
	StartSkewLimit       time.Duration
	ResourceSampleGap    time.Duration
}

type RunMetadata struct {
	SchemaVersion          string            `json:"schema_version"`
	RunID                  string            `json:"run_id"`
	CreatedAt              string            `json:"created_at"`
	Calibration            bool              `json:"calibration"`
	GitCommit              string            `json:"git_commit"`
	ContainerImage         string            `json:"container_image"`
	GoVersion              string            `json:"go_version"`
	OS                     string            `json:"os"`
	Architecture           string            `json:"architecture"`
	Hostname               string            `json:"hostname"`
	Transformer            string            `json:"transformer"`
	FixturePath            string            `json:"fixture_path"`
	FixtureSHA256          string            `json:"fixture_sha256"`
	FixtureBytes           int               `json:"fixture_bytes"`
	SourceHash             string            `json:"source_hash"`
	HotCanonicalSpec       string            `json:"hot_canonical_spec"`
	UnrelatedCanonicalSpec string            `json:"unrelated_canonical_spec"`
	Repetitions            int               `json:"repetitions"`
	TransformConcurrency   int               `json:"transform_concurrency"`
	RequestTimeoutMS       float64           `json:"request_timeout_ms"`
	TransformTimeoutMS     float64           `json:"transform_timeout_ms"`
	StartSkewLimitMS       float64           `json:"start_skew_limit_ms"`
	ResourceSampleGapMS    float64           `json:"resource_sample_gap_ms"`
	CPUQuota               string            `json:"cpu_quota"`
	MemoryLimitBytes       int64             `json:"memory_limit_bytes"`
	ExecutionOrder         []string          `json:"execution_order"`
	Commands               []string          `json:"commands"`
	Environment            map[string]string `json:"environment"`
	KnownLimitations       []string          `json:"known_limitations"`
}

func NewRunMetadata(config Config) RunMetadata {
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
		Repetitions:          config.Repetitions,
		TransformConcurrency: config.TransformConcurrency,
		RequestTimeoutMS:     milliseconds(config.RequestTimeout),
		TransformTimeoutMS:   milliseconds(config.TransformTimeout),
		StartSkewLimitMS:     milliseconds(config.StartSkewLimit),
		ResourceSampleGapMS:  milliseconds(config.ResourceSampleGap),
		Environment:          make(map[string]string),
	}
}

type RequestResult struct {
	RunID          string
	TrialID        string
	Scenario       string
	RequestID      string
	RequestClass   string
	RequestRole    string
	TargetTaskID   string
	TaskID         string
	ImageKey       string
	StartedAt      string
	FinishedAt     string
	LatencyMS      float64
	HTTPStatus     int
	ResponseSHA256 string
	ErrorType      string
	Coalesced      bool
	Cache          string
}

type TrialResult struct {
	RunID                string
	TrialID              string
	ColdStateID          string
	Scenario             string
	Repetition           int
	Concurrency          int
	ProcessCount         int
	HotRequests          int
	UnrelatedRequests    int
	WarmUnrelated        bool
	Valid                bool
	InvalidReason        string
	StartSkewMS          float64
	TotalRequests        int
	SuccessRequests      int
	ErrorRequests        int
	CanceledRequests     int
	DerivativeHits       int64
	DerivativeMisses     int64
	TransformAttempts    int64
	TransformMaxInflight int64
	TransformInflight    int64
	OriginalReads        int64
	PublishCreated       int64
	PublishExisting      int64
	PublishErrors        int64
	CoalescedRequests    int64
	ActiveTasks          int
	DerivativeFiles      int
	P50MS                float64
	P95MS                float64
	P99MS                float64
	CPUTimeMS            float64
	PeakRSSBytes         int64
	PeakCgroupMemBytes   int64
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

type MetricRecord struct {
	TrialID  string
	Scenario string
	TaskID   string
	Snapshot media.MetricsSnapshot
}

type Event struct {
	RunID     string
	TrialID   string
	Scenario  string
	Name      string
	RequestID string
	Timestamp string
	Detail    string
}

type RunOutput struct {
	Directory string
	Metadata  RunMetadata
	Trials    []TrialResult
	Requests  []RequestResult
	Resources []ResourceSample
	Metrics   []MetricRecord
	Events    []Event
}

func milliseconds(duration time.Duration) float64 {
	return float64(duration) / float64(time.Millisecond)
}
