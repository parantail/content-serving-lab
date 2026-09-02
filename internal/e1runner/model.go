package e1runner

import (
	"runtime"
	"time"
)

const SchemaVersion = "e1-v1"

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
	FixturePath          string            `json:"fixture_path"`
	FixtureSHA256        string            `json:"fixture_sha256"`
	FixtureBytes         int               `json:"fixture_bytes"`
	SourceHash           string            `json:"source_hash"`
	CanonicalSpec        string            `json:"canonical_spec"`
	DerivativeWidth      int               `json:"derivative_width"`
	DerivativeHeight     int               `json:"derivative_height"`
	DerivativeFormat     string            `json:"derivative_format"`
	DerivativeQuality    int               `json:"derivative_quality"`
	Repetitions          int               `json:"repetitions"`
	TransformConcurrency int               `json:"transform_concurrency"`
	RequestTimeoutMS     float64           `json:"request_timeout_ms"`
	TransformTimeoutMS   float64           `json:"transform_timeout_ms"`
	StartSkewLimitMS     float64           `json:"start_skew_limit_ms"`
	ResourceSampleGapMS  float64           `json:"resource_sample_gap_ms"`
	CPUQuota             string            `json:"cpu_quota"`
	MemoryLimitBytes     int64             `json:"memory_limit_bytes"`
	ExecutionOrder       []string          `json:"execution_order"`
	Commands             []string          `json:"commands"`
	Environment          map[string]string `json:"environment"`
	KnownLimitations     []string          `json:"known_limitations"`
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
	ImageKey       string
	TaskID         string
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
	RunID              string
	TrialID            string
	ColdStateID        string
	Scenario           string
	Mode               string
	Repetition         int
	Concurrency        int
	Valid              bool
	InvalidReason      string
	StartSkewMS        float64
	TotalRequests      int
	SuccessRequests    int
	ErrorRequests      int
	TransformAttempts  int64
	OriginalReads      int64
	PublishCreated     int64
	PublishExisting    int64
	CoalescedRequests  int64
	P50MS              float64
	P95MS              float64
	P99MS              float64
	CPUTimeMS          float64
	PeakRSSBytes       int64
	PeakCgroupMemBytes int64
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

func milliseconds(duration time.Duration) float64 {
	return float64(duration) / float64(time.Millisecond)
}
