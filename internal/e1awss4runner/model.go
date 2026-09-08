package e1awss4runner

import (
	"time"

	"github.com/parantail/content-serving-lab/internal/e1awss4"
)

const SchemaVersion = "e1-aws-s4-v1"

const (
	ScenarioTask1 = "S4-AWS-1-CONTROL"
	ScenarioTask2 = "S4-AWS-2"
	ScenarioTask4 = "S4-AWS-4"
)

const RequestsPerTrial = 100

type Config struct {
	RunID             string
	ResultsRoot       string
	Region            string
	GitCommit         string
	ContainerDigest   string
	Calibration       bool
	Repetitions       int
	SourceHash        string
	CanonicalSpec     string
	DerivativeKey     string
	DerivativeBucket  string
	ResultBucket      string
	ResultPrefix      string
	Services          []ServiceTarget
	RequestTimeout    time.Duration
	ControlTimeout    time.Duration
	ControlPollGap    time.Duration
	StartSkewLimit    time.Duration
	ExpectedWebPBytes int64
}

type ServiceTarget struct {
	Scenario       string
	Repetition     int
	Endpoint       string
	Cluster        string
	Service        string
	TargetGroupARN string
	ListenerPort   int
	ExpectedTasks  int
}

type RunMetadata struct {
	MeasurementContract string   `json:"measurement_contract,omitempty"`
	ResourceSampleGapMS float64  `json:"resource_sample_gap_ms,omitempty"`
	SchemaVersion       string   `json:"schema_version"`
	RunID               string   `json:"run_id"`
	CreatedAt           string   `json:"created_at"`
	Calibration         bool     `json:"calibration"`
	GitCommit           string   `json:"git_commit"`
	ContainerDigest     string   `json:"container_image_digest"`
	Region              string   `json:"region"`
	SourceHash          string   `json:"source_hash"`
	CanonicalSpec       string   `json:"canonical_spec"`
	DerivativeKey       string   `json:"derivative_key"`
	RequestsPerTrial    int      `json:"requests_per_trial"`
	Repetitions         int      `json:"repetitions"`
	RequestTimeoutMS    float64  `json:"request_timeout_ms"`
	ControlTimeoutMS    float64  `json:"control_timeout_ms"`
	ControlPollGapMS    float64  `json:"control_poll_gap_ms"`
	StartSkewLimitMS    float64  `json:"start_skew_limit_ms"`
	ExpectedWebPBytes   int64    `json:"expected_webp_bytes,omitempty"`
	ExecutionOrder      []string `json:"execution_order"`
	KnownLimitations    []string `json:"known_limitations"`
	SanitizationApplied bool     `json:"sanitization_applied"`
}

type Infrastructure struct {
	SchemaVersion string                  `json:"schema_version"`
	RunID         string                  `json:"run_id"`
	Region        string                  `json:"region"`
	Services      []InfrastructureService `json:"services"`
	Tasks         []e1awss4.TaskIdentity  `json:"tasks"`
}

type InfrastructureService struct {
	Scenario        string `json:"scenario"`
	ExpectedTasks   int    `json:"expected_tasks"`
	ListenerPort    int    `json:"listener_port"`
	TargetAlgorithm string `json:"target_algorithm"`
	Stickiness      bool   `json:"stickiness"`
}

type CostDocument struct {
	SchemaVersion          string  `json:"schema_version"`
	RunID                  string  `json:"run_id"`
	Status                 string  `json:"status"`
	PricingAsOf            string  `json:"pricing_as_of,omitempty"`
	MeasuredTaskCPUSeconds float64 `json:"measured_task_cpu_seconds"`
	DerivativeGETRequests  int64   `json:"derivative_get_requests"`
	OriginalGETRequests    int64   `json:"original_get_requests"`
	ConditionalPUTRequests int64   `json:"conditional_put_requests"`
	PublishAttemptBytes    int64   `json:"publish_attempt_bytes"`
	Note                   string  `json:"note"`
}

type HealthSnapshot struct {
	Stable         bool
	DesiredTasks   int
	RunningTasks   int
	PendingTasks   int
	HealthyTaskIDs []string
}

type TrialRecord struct {
	RunID                  string
	TrialID                string
	Scenario               string
	Repetition             int
	ExpectedTasks          int
	DeploymentStableBefore bool
	DeploymentStableAfter  bool
	HealthyBeforeTaskIDs   []string
	HealthyAfterTaskIDs    []string
	PreparedTaskIDs        []string
	FinishedTaskIDs        []string
	ColdConfirmed          bool
	TotalRequests          int
	SuccessRequests        int
	HTTP5xx                int
	Timeouts               int
	ConnectionErrors       int
	StartSkewMS            float64
	ResponseSHA256         string
	ResponseBytes          int64
	StoredSHA256           string
	StoredBytes            int64
	StoredWebPValid        bool
	ImageRequests          int64
	DerivativeGetHit       int64
	DerivativeGetMiss      int64
	DerivativeGetError     int64
	DerivativeGetBytes     int64
	OriginalGetCount       int64
	OriginalGetBytes       int64
	TransformAttempts      int64
	TransformSuccess       int64
	TransformFailure       int64
	TransformDurationNanos int64
	TransformMaxInflight   int64
	TransformInflight      int64
	CoalescedRequests      int64
	PublishCreated         int64
	PublishExisting        int64
	PublishConflict        int64
	PublishError           int64
	PublishAttemptBytes    int64
	CoordinatorKeys        int
	CoordinatorWaiters     int
	CPUUsageNanos          uint64
	PeakMemoryBytes        uint64
	ResourceErrors         int64
	P50MS                  float64
	P95MS                  float64
	P99MS                  float64
	Valid                  bool
	InvalidReason          string
}

type RequestRecord struct {
	RunID            string
	TrialID          string
	Scenario         string
	RequestID        string
	TaskID           string
	AvailabilityZone string
	StartedAt        string
	FinishedAt       string
	LatencyMS        float64
	HTTPStatus       int
	ResponseBytes    int64
	ResponseSHA256   string
	DerivativeKey    string
	Cache            string
	Coalesced        bool
	TrialHeader      string
	ErrorType        string
}

type TaskRecord struct {
	RunID          string
	TrialID        string
	Scenario       string
	Task           e1awss4.TaskIdentity
	PreparedAt     string
	FinishedAt     string
	FirstRequestAt string
	LastRequestAt  string
	Counters       e1awss4.TrialCounters
}

type ResourceRecord struct {
	RunID            string
	TrialID          string
	Scenario         string
	TaskID           string
	Timestamp        string
	CPUUsageNanos    uint64
	MemoryUsageBytes uint64
	Error            string
}

type StorageRecord struct {
	RunID     string
	TrialID   string
	Scenario  string
	Actor     string
	TaskID    string
	Operation string
	Result    string
	Requests  int64
	Bytes     int64
}

type EventRecord struct {
	RunID     string
	TrialID   string
	Scenario  string
	Name      string
	Timestamp string
	Detail    string
}

type RunOutput struct {
	Directory      string
	Metadata       RunMetadata
	Infrastructure Infrastructure
	Trials         []TrialRecord
	Requests       []RequestRecord
	Tasks          []TaskRecord
	Resources      []ResourceRecord
	Storage        []StorageRecord
	Events         []EventRecord
	Cost           CostDocument
}

type AnalysisOutput struct {
	Directory     string
	RunID         string
	ValidTrials   int
	InvalidTrials int
}

func milliseconds(duration time.Duration) float64 {
	return float64(duration) / float64(time.Millisecond)
}
