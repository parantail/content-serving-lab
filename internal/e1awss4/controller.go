package e1awss4

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/parantail/content-serving-lab/internal/media"
)

const (
	SchemaVersion = "e1-aws-s4-task-v1"
	TaskIDHeader  = "X-E1-Task-ID"
	TrialIDHeader = "X-E1-Trial-ID"
)

var (
	ErrInvalidTrialID   = errors.New("invalid experiment trial ID")
	ErrTrialConflict    = errors.New("another experiment trial is active")
	ErrTrialNotPrepared = errors.New("experiment trial is not prepared on this task")
	ErrTrialNotDrained  = errors.New("experiment trial still has active work")
	ErrTrialFinished    = errors.New("experiment trial already finished on this task")
)

type TrialCounters struct {
	ImageRequests          int64  `json:"image_requests"`
	RequestsInflight       int64  `json:"requests_inflight"`
	DerivativeGetHit       int64  `json:"derivative_get_hit"`
	DerivativeGetMiss      int64  `json:"derivative_get_miss"`
	DerivativeGetError     int64  `json:"derivative_get_error"`
	DerivativeGetBytes     int64  `json:"derivative_get_bytes"`
	OriginalGetCount       int64  `json:"original_get_count"`
	OriginalGetSuccess     int64  `json:"original_get_success"`
	OriginalGetError       int64  `json:"original_get_error"`
	OriginalGetBytes       int64  `json:"original_get_bytes"`
	TransformAttempts      int64  `json:"transform_attempts"`
	TransformSuccess       int64  `json:"transform_success"`
	TransformFailure       int64  `json:"transform_failure"`
	TransformError         int64  `json:"transform_error"`
	TransformTimeout       int64  `json:"transform_timeout"`
	TransformDurationNanos int64  `json:"transform_duration_nanos"`
	TransformInflight      int64  `json:"transform_inflight"`
	TransformMaxInflight   int64  `json:"transform_max_inflight"`
	CoalescedRequests      int64  `json:"coalesced_requests"`
	PublishCreated         int64  `json:"publish_created"`
	PublishExisting        int64  `json:"publish_existing"`
	PublishConflict        int64  `json:"publish_conflict"`
	PublishError           int64  `json:"publish_error"`
	PublishAttemptBytes    int64  `json:"publish_attempt_bytes"`
	CoordinatorKeys        int    `json:"coordinator_keys"`
	CoordinatorWaiters     int    `json:"coordinator_waiters"`
	CPUUsageNanos          uint64 `json:"cpu_usage_nanos"`
	PeakMemoryBytes        uint64 `json:"peak_memory_bytes"`
	ResourceErrors         int64  `json:"resource_errors"`
}

type TrialReport struct {
	Schema         string           `json:"schema"`
	State          string           `json:"state"`
	TrialID        string           `json:"trial_id"`
	Task           TaskIdentity     `json:"task"`
	PreparedAt     string           `json:"prepared_at"`
	FinishedAt     string           `json:"finished_at,omitempty"`
	FirstRequestAt string           `json:"first_request_at,omitempty"`
	LastRequestAt  string           `json:"last_request_at,omitempty"`
	Counters       TrialCounters    `json:"counters"`
	Resources      []ResourceSample `json:"resources"`
}

type Controller struct {
	identity       TaskIdentity
	processor      *media.Processor
	storageMetrics *media.S3Metrics
	resourceSource ResourceSource
	sampleGap      time.Duration

	mu       sync.Mutex
	active   *activeTrial
	finished map[string]TrialReport
}

type activeTrial struct {
	id               string
	preparedAt       time.Time
	firstRequestAt   time.Time
	lastRequestAt    time.Time
	imageRequests    int64
	requestsInflight int64
	finishing        bool
	mediaBaseline    media.MetricsSnapshot
	storageBaseline  media.S3MetricsSnapshot
	sampler          *resourceSampler
}

func NewController(identity TaskIdentity, processor *media.Processor, storageMetrics *media.S3Metrics, resourceSource ResourceSource, sampleGap time.Duration) (*Controller, error) {
	if identity.TaskID == "" {
		return nil, errors.New("experiment task identity is required")
	}
	if processor == nil || storageMetrics == nil || resourceSource == nil {
		return nil, errors.New("experiment controller dependencies are required")
	}
	if sampleGap <= 0 {
		return nil, errors.New("experiment resource sample interval must be positive")
	}
	return &Controller{
		identity: identity, processor: processor, storageMetrics: storageMetrics,
		resourceSource: resourceSource, sampleGap: sampleGap,
		finished: make(map[string]TrialReport),
	}, nil
}

func (c *Controller) Identity() TaskIdentity {
	return c.identity
}

func (c *Controller) Prepare(ctx context.Context, trialID string) (TrialReport, error) {
	if err := validateTrialID(trialID); err != nil {
		return TrialReport{}, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active != nil {
		if c.active.id == trialID && !c.active.finishing {
			return c.preparedReport(c.active), nil
		}
		return TrialReport{}, ErrTrialConflict
	}
	if _, exists := c.finished[trialID]; exists {
		return TrialReport{}, ErrTrialFinished
	}
	if state := c.processor.State(); state.TransformInflight != 0 || state.CoordinatorKeys != 0 || state.CoordinatorWaiters != 0 {
		return TrialReport{}, fmt.Errorf("%w before prepare", ErrTrialNotDrained)
	}
	if err := c.processor.Metrics().ResetTransformMaxInflight(); err != nil {
		return TrialReport{}, err
	}
	sampler, err := startResourceSampler(ctx, c.resourceSource, c.sampleGap)
	if err != nil {
		return TrialReport{}, fmt.Errorf("start experiment resource sampler: %w", err)
	}
	c.active = &activeTrial{
		id:              trialID,
		preparedAt:      time.Now().UTC(),
		mediaBaseline:   c.processor.Metrics().Snapshot(),
		storageBaseline: c.storageMetrics.Snapshot(),
		sampler:         sampler,
	}
	return c.preparedReport(c.active), nil
}

func (c *Controller) BeginRequest(trialID string) (func(), error) {
	if err := validateTrialID(trialID); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.active == nil || c.active.id != trialID {
		c.mu.Unlock()
		return nil, ErrTrialNotPrepared
	}
	if c.active.finishing {
		c.mu.Unlock()
		return nil, ErrTrialConflict
	}
	now := time.Now().UTC()
	if c.active.firstRequestAt.IsZero() {
		c.active.firstRequestAt = now
	}
	c.active.lastRequestAt = now
	c.active.imageRequests++
	c.active.requestsInflight++
	c.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			if c.active != nil && c.active.id == trialID {
				c.active.requestsInflight--
			}
			c.mu.Unlock()
		})
	}, nil
}

func (c *Controller) Finish(trialID string) (TrialReport, error) {
	if err := validateTrialID(trialID); err != nil {
		return TrialReport{}, err
	}

	c.mu.Lock()
	if report, exists := c.finished[trialID]; exists {
		c.mu.Unlock()
		return cloneReport(report), nil
	}
	if c.active == nil || c.active.id != trialID {
		c.mu.Unlock()
		return TrialReport{}, ErrTrialNotPrepared
	}
	state := c.processor.State()
	if c.active.requestsInflight != 0 || state.TransformInflight != 0 || state.CoordinatorKeys != 0 || state.CoordinatorWaiters != 0 {
		c.mu.Unlock()
		return TrialReport{}, ErrTrialNotDrained
	}
	if c.active.finishing {
		c.mu.Unlock()
		return TrialReport{}, ErrTrialConflict
	}
	c.active.finishing = true
	active := c.active
	c.mu.Unlock()

	resources := active.sampler.finish()
	finishedAt := time.Now().UTC()

	c.mu.Lock()
	defer c.mu.Unlock()
	report := TrialReport{
		Schema:         SchemaVersion,
		State:          "finished",
		TrialID:        trialID,
		Task:           c.identity,
		PreparedAt:     active.preparedAt.Format(time.RFC3339Nano),
		FinishedAt:     finishedAt.Format(time.RFC3339Nano),
		FirstRequestAt: formatOptionalTime(active.firstRequestAt),
		LastRequestAt:  formatOptionalTime(active.lastRequestAt),
		Counters:       c.finishedCounters(active, resources),
		Resources:      resources,
	}
	c.finished[trialID] = report
	c.active = nil
	return cloneReport(report), nil
}

func (c *Controller) preparedReport(active *activeTrial) TrialReport {
	return TrialReport{
		Schema:     SchemaVersion,
		State:      "prepared",
		TrialID:    active.id,
		Task:       c.identity,
		PreparedAt: active.preparedAt.Format(time.RFC3339Nano),
		Resources:  active.sampler.snapshot(),
	}
}

func (c *Controller) finishedCounters(active *activeTrial, resources []ResourceSample) TrialCounters {
	metrics := c.processor.Metrics().Snapshot()
	storage := c.storageMetrics.Snapshot().Subtract(active.storageBaseline)
	state := c.processor.State()
	transformSuccess := metrics.TransformSuccess - active.mediaBaseline.TransformSuccess
	transformError := metrics.TransformError - active.mediaBaseline.TransformError
	transformTimeout := metrics.TransformTimeout - active.mediaBaseline.TransformTimeout
	counters := TrialCounters{
		ImageRequests:          active.imageRequests,
		RequestsInflight:       active.requestsInflight,
		DerivativeGetHit:       storage.DerivativeGetHit,
		DerivativeGetMiss:      storage.DerivativeGetMiss,
		DerivativeGetError:     storage.DerivativeGetError,
		DerivativeGetBytes:     storage.DerivativeGetBytes,
		OriginalGetCount:       storage.OriginalGetSuccess + storage.OriginalGetError,
		OriginalGetSuccess:     storage.OriginalGetSuccess,
		OriginalGetError:       storage.OriginalGetError,
		OriginalGetBytes:       storage.OriginalGetBytes,
		TransformAttempts:      transformSuccess + transformError + transformTimeout,
		TransformSuccess:       transformSuccess,
		TransformFailure:       transformError + transformTimeout,
		TransformError:         transformError,
		TransformTimeout:       transformTimeout,
		TransformDurationNanos: metrics.TransformDurationNanos - active.mediaBaseline.TransformDurationNanos,
		TransformInflight:      state.TransformInflight,
		TransformMaxInflight:   metrics.TransformMaxInflight,
		CoalescedRequests:      metrics.Coalesced - active.mediaBaseline.Coalesced,
		PublishCreated:         storage.PublishCreated,
		PublishExisting:        storage.PublishExisting,
		PublishConflict:        storage.PublishConflict,
		PublishError:           storage.PublishError,
		PublishAttemptBytes:    storage.PublishAttemptBytes,
		CoordinatorKeys:        state.CoordinatorKeys,
		CoordinatorWaiters:     state.CoordinatorWaiters,
	}
	var firstCPU, lastCPU uint64
	firstCPUSet := false
	for _, sample := range resources {
		if sample.Error != "" {
			counters.ResourceErrors++
			continue
		}
		if !firstCPUSet {
			firstCPU = sample.CPUUsageNanos
			firstCPUSet = true
		}
		lastCPU = sample.CPUUsageNanos
		if sample.MemoryUsageBytes > counters.PeakMemoryBytes {
			counters.PeakMemoryBytes = sample.MemoryUsageBytes
		}
	}
	if firstCPUSet && lastCPU >= firstCPU {
		counters.CPUUsageNanos = lastCPU - firstCPU
	}
	return counters
}

func validateTrialID(trialID string) error {
	if len(trialID) < 1 || len(trialID) > 128 {
		return ErrInvalidTrialID
	}
	for _, character := range trialID {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '-' && character != '_' && character != '.' {
			return ErrInvalidTrialID
		}
	}
	return nil
}

func formatOptionalTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339Nano)
}

func cloneReport(report TrialReport) TrialReport {
	report.Resources = append([]ResourceSample(nil), report.Resources...)
	return report
}
