package e1awss4

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/parantail/content-serving-lab/internal/media"
)

const testSourceHash = "de206136ed0eeaa42499840f49fa35bd3d57745b8a9580b16f0d6e6393ac7f91"

func TestControllerReportsTrialCountersAndResources(t *testing.T) {
	processor := newTestProcessor(t)
	resources := &sequenceResourceSource{points: []ResourcePoint{
		{CPUUsageNanos: 100, MemoryUsageBytes: 1_000},
		{CPUUsageNanos: 250, MemoryUsageBytes: 1_800},
	}}
	controller := newTestController(t, processor, &media.S3Metrics{}, resources)

	prepared, err := controller.Prepare(context.Background(), "trial-001")
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Schema != SchemaVersion || prepared.State != "prepared" || prepared.Task.TaskID != "task-001" || len(prepared.Resources) != 1 {
		t.Fatalf("prepared report = %+v", prepared)
	}
	idempotent, err := controller.Prepare(context.Background(), "trial-001")
	if err != nil || idempotent.PreparedAt != prepared.PreparedAt {
		t.Fatalf("idempotent Prepare() = %+v, %v", idempotent, err)
	}

	finishRequest, err := controller.BeginRequest("trial-001")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := media.ParseTransformSpec("width=640,height=640,fit=cover,quality=80", "webp")
	if err != nil {
		t.Fatal(err)
	}
	result, err := processor.GetDerivative(context.Background(), testSourceHash, spec)
	if err != nil || string(result.Data) != "webp-result" {
		t.Fatalf("GetDerivative() = %q, %v", result.Data, err)
	}
	finishRequest()
	finishRequest()

	report, err := controller.Finish("trial-001")
	if err != nil {
		t.Fatal(err)
	}
	if report.State != "finished" || report.FirstRequestAt == "" || report.LastRequestAt == "" || report.FinishedAt == "" {
		t.Fatalf("finished report times/state = %+v", report)
	}
	if report.Counters.ImageRequests != 1 || report.Counters.RequestsInflight != 0 {
		t.Fatalf("request counters = %+v", report.Counters)
	}
	if report.Counters.TransformAttempts != 1 || report.Counters.TransformSuccess != 1 || report.Counters.TransformFailure != 0 || report.Counters.TransformMaxInflight != 1 {
		t.Fatalf("transform counters = %+v", report.Counters)
	}
	if report.Counters.TransformDurationNanos <= 0 || report.Counters.CoordinatorKeys != 0 || report.Counters.CoordinatorWaiters != 0 {
		t.Fatalf("drained transform state = %+v", report.Counters)
	}
	if report.Counters.CPUUsageNanos != 150 || report.Counters.PeakMemoryBytes != 1_800 || report.Counters.ResourceErrors != 0 {
		t.Fatalf("resource counters = %+v", report.Counters)
	}
	if len(report.Resources) != 2 {
		t.Fatalf("resource samples = %d, want 2", len(report.Resources))
	}

	repeated, err := controller.Finish("trial-001")
	if err != nil || !reflect.DeepEqual(repeated, report) {
		t.Fatalf("idempotent Finish() = %+v, %v; want %+v", repeated, err, report)
	}
	if _, err := controller.Prepare(context.Background(), "trial-001"); !errors.Is(err, ErrTrialFinished) {
		t.Fatalf("Prepare(finished trial) error = %v, want ErrTrialFinished", err)
	}

	if _, err := controller.Prepare(context.Background(), "trial-002"); err != nil {
		t.Fatal(err)
	}
	second, err := controller.Finish("trial-002")
	if err != nil {
		t.Fatal(err)
	}
	if second.Counters.TransformMaxInflight != 0 || second.Counters.TransformAttempts != 0 {
		t.Fatalf("second trial transform counters were not reset: %+v", second.Counters)
	}
}

func TestControllerRefusesFinishUntilRequestsDrain(t *testing.T) {
	processor := newTestProcessor(t)
	controller := newTestController(t, processor, &media.S3Metrics{}, &sequenceResourceSource{
		points: []ResourcePoint{{CPUUsageNanos: 100}, {CPUUsageNanos: 110}},
	})
	if _, err := controller.Prepare(context.Background(), "trial-active"); err != nil {
		t.Fatal(err)
	}
	finishRequest, err := controller.BeginRequest("trial-active")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Finish("trial-active"); !errors.Is(err, ErrTrialNotDrained) {
		t.Fatalf("Finish() error = %v, want ErrTrialNotDrained", err)
	}
	finishRequest()
	if _, err := controller.Finish("trial-active"); err != nil {
		t.Fatal(err)
	}
}

func TestControllerReportsS3CountersForItsTrial(t *testing.T) {
	derivativeKey := testDerivativeKey(t)
	var derivativeCreated atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/original-bucket/originals/"+testSourceHash:
			_, _ = io.WriteString(w, "original")
		case r.Method == http.MethodGet && r.URL.Path == "/derivative-bucket/derivatives/"+derivativeKey+".webp":
			if !derivativeCreated.Load() {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `<Error><Code>NoSuchKey</Code></Error>`)
				return
			}
			_, _ = io.WriteString(w, "webp-result")
		case r.Method == http.MethodPut && r.URL.Path == "/derivative-bucket/derivatives/"+derivativeKey+".webp":
			derivativeCreated.Store(true)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	storageMetrics := &media.S3Metrics{}
	client := s3.New(s3.Options{
		BaseEndpoint: aws.String(server.URL),
		Credentials:  aws.AnonymousCredentials{},
		Region:       "ap-northeast-2",
		Retryer:      aws.NopRetryer{},
		UsePathStyle: true,
	})
	originals, err := media.NewS3OriginalStore(client, "original-bucket", storageMetrics)
	if err != nil {
		t.Fatal(err)
	}
	derivatives, err := media.NewS3DerivativeStore(client, "derivative-bucket", storageMetrics)
	if err != nil {
		t.Fatal(err)
	}
	processor := media.NewProcessor(
		originals,
		derivatives,
		&media.DeterministicTransformer{Output: []byte("webp-result")},
		media.NewProcessCoordinator(time.Second),
		&media.Metrics{},
	)
	controller := newTestController(t, processor, storageMetrics, &sequenceResourceSource{
		points: []ResourcePoint{{CPUUsageNanos: 100}, {CPUUsageNanos: 200}},
	})
	if _, err := controller.Prepare(context.Background(), "trial-s3"); err != nil {
		t.Fatal(err)
	}
	finishRequest, err := controller.BeginRequest("trial-s3")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := media.ParseTransformSpec("width=640,height=640,fit=cover,quality=80", "webp")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := processor.GetDerivative(context.Background(), testSourceHash, spec); err != nil {
		t.Fatal(err)
	}
	finishRequest()
	report, err := controller.Finish("trial-s3")
	if err != nil {
		t.Fatal(err)
	}
	counters := report.Counters
	if counters.DerivativeGetMiss != 2 || counters.DerivativeGetHit != 0 || counters.DerivativeGetError != 0 {
		t.Fatalf("derivative S3 counters = %+v", counters)
	}
	if counters.OriginalGetCount != 1 || counters.OriginalGetSuccess != 1 || counters.OriginalGetBytes != int64(len("original")) {
		t.Fatalf("original S3 counters = %+v", counters)
	}
	if counters.PublishCreated != 1 || counters.PublishExisting != 0 || counters.PublishConflict != 0 || counters.PublishError != 0 || counters.PublishAttemptBytes != int64(len("webp-result")) {
		t.Fatalf("publish S3 counters = %+v", counters)
	}
}

func testDerivativeKey(t *testing.T) string {
	t.Helper()
	spec, err := media.ParseTransformSpec("width=640,height=640,fit=cover,quality=80", "webp")
	if err != nil {
		t.Fatal(err)
	}
	key, err := media.DerivativeKey(testSourceHash, spec)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func newTestController(t *testing.T, processor *media.Processor, storageMetrics *media.S3Metrics, resources ResourceSource) *Controller {
	t.Helper()
	controller, err := NewController(TaskIdentity{
		TaskID:                 "task-001",
		TaskDefinitionFamily:   "e1-media-service",
		TaskDefinitionRevision: "7",
		AvailabilityZone:       "ap-northeast-2a",
		LaunchType:             "FARGATE",
		CPUVCpu:                1,
		MemoryMiB:              2048,
		ImageDigest:            testImageDigest,
	}, processor, storageMetrics, resources, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return controller
}

func newTestProcessor(t *testing.T) *media.Processor {
	t.Helper()
	directory := t.TempDir()
	originalPath := filepath.Join(directory, "original.jpg")
	if err := os.WriteFile(originalPath, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	derivatives, err := media.NewLocalDerivativeStore(filepath.Join(directory, "derivatives"))
	if err != nil {
		t.Fatal(err)
	}
	return media.NewProcessor(
		media.NewFileOriginalStore(map[string]string{testSourceHash: originalPath}),
		derivatives,
		&media.DeterministicTransformer{Delay: time.Millisecond, Output: []byte("webp-result")},
		media.NewProcessCoordinator(time.Second),
		&media.Metrics{},
	)
}

type sequenceResourceSource struct {
	mu     sync.Mutex
	points []ResourcePoint
	next   int
}

func (s *sequenceResourceSource) Sample(context.Context) (ResourcePoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.points) == 0 {
		return ResourcePoint{}, errors.New("no resource samples")
	}
	index := s.next
	if index >= len(s.points) {
		index = len(s.points) - 1
	}
	s.next++
	return s.points[index], nil
}
