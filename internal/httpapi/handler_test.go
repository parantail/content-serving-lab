package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/parantail/content-serving-lab/internal/e1awss4"
	"github.com/parantail/content-serving-lab/internal/httpapi"
	"github.com/parantail/content-serving-lab/internal/media"
)

const sourceHash = "de206136ed0eeaa42499840f49fa35bd3d57745b8a9580b16f0d6e6393ac7f91"

func TestHealthEndpoints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		body string
	}{
		{name: "live", path: "/health/live", body: "{\"status\":\"live\"}\n"},
		{name: "ready", path: "/health/ready", body: "{\"status\":\"ready\"}\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, tt.path, nil)

			httpapi.NewHandler(nil).ServeHTTP(recorder, request)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
			}
			if got := recorder.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", got)
			}
			if got := recorder.Body.String(); got != tt.body {
				t.Fatalf("body = %q, want %q", got, tt.body)
			}
		})
	}
}

func TestHealthEndpointRejectsPost(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/health/ready", nil)

	httpapi.NewHandler(nil).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
	}
}

func TestDerivativeEndpointUsesCanonicalKeyAndReturnsCacheHeaders(t *testing.T) {
	t.Parallel()

	processor, transformer := newProcessor(t)
	handler := httpapi.NewHandler(processor)
	paths := []string{
		"/i/" + sourceHash + "/width=640,height=640,fit=cover,quality=80.webp",
		"/i/" + sourceHash + "/quality=80,fit=cover,height=640,width=640.webp",
	}

	var firstKey string
	for index, path := range paths {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		handler.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("request %d status = %d, body = %s", index, recorder.Code, recorder.Body.String())
		}
		if recorder.Header().Get("Content-Type") != "image/webp" {
			t.Fatalf("Content-Type = %q", recorder.Header().Get("Content-Type"))
		}
		if !strings.Contains(recorder.Header().Get("Cache-Control"), "immutable") {
			t.Fatalf("Cache-Control = %q", recorder.Header().Get("Cache-Control"))
		}
		if recorder.Header().Get("ETag") == "" {
			t.Fatal("ETag is empty")
		}
		if index == 0 {
			firstKey = recorder.Header().Get("X-Derivative-Key")
		} else if recorder.Header().Get("X-Derivative-Key") != firstKey {
			t.Fatalf("equivalent request key changed: %q != %q", recorder.Header().Get("X-Derivative-Key"), firstKey)
		}
	}
	if transformer.Calls() != 1 {
		t.Fatalf("transformer calls = %d, want 1", transformer.Calls())
	}
}

func TestDerivativeEndpointRejectsInvalidSpecBeforeTransform(t *testing.T) {
	t.Parallel()

	processor, transformer := newProcessor(t)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/i/"+sourceHash+"/width=0,height=640,fit=cover,quality=80.webp", nil)

	httpapi.NewHandler(processor).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if transformer.Calls() != 0 {
		t.Fatalf("transformer calls = %d, want 0", transformer.Calls())
	}
}

func TestMetricsEndpointReportsPipelineCounters(t *testing.T) {
	t.Parallel()

	processor, _ := newProcessor(t)
	spec, err := media.ParseTransformSpec("width=640,height=640,fit=cover,quality=80", "webp")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := processor.GetDerivative(context.Background(), sourceHash, spec); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	httpapi.NewHandler(processor).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	for _, expected := range []string{
		`media_derivative_requests_total{result="miss"} 1`,
		`media_original_reads_total{result="success"} 1`,
		`media_transform_attempts_total{result="success"} 1`,
		`media_derivative_publish_attempts_total{result="created"} 1`,
	} {
		if !strings.Contains(recorder.Body.String(), expected) {
			t.Errorf("metrics missing %q:\n%s", expected, recorder.Body.String())
		}
	}
}

func TestExperimentEndpointsAreDisabledByDefault(t *testing.T) {
	t.Parallel()

	processor, _ := newProcessor(t)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/internal/e1/trials/trial-001/prepare", nil)

	httpapi.NewHandler(processor).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func TestExperimentPrepareImageAndFinishFlow(t *testing.T) {
	t.Parallel()

	processor, _ := newProcessor(t)
	controller, err := e1awss4.NewController(e1awss4.TaskIdentity{
		TaskID:                 "task-001",
		TaskDefinitionFamily:   "e1-media-service",
		TaskDefinitionRevision: "7",
		AvailabilityZone:       "ap-northeast-2a",
		LaunchType:             "FARGATE",
		CPUVCpu:                1,
		MemoryMiB:              2048,
		ImageDigest:            "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}, processor, &media.S3Metrics{}, &increasingResourceSource{}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.NewHandlerWithExperiment(processor, controller)

	prepare := httptest.NewRecorder()
	handler.ServeHTTP(prepare, httptest.NewRequest(http.MethodPost, "/internal/e1/trials/trial-001/prepare", nil))
	if prepare.Code != http.StatusOK {
		t.Fatalf("prepare status = %d, body = %s", prepare.Code, prepare.Body.String())
	}
	assertExperimentHeaders(t, prepare, "trial-001")
	if got := prepare.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("prepare Cache-Control = %q, want no-store", got)
	}
	var prepared e1awss4.TrialReport
	if err := json.Unmarshal(prepare.Body.Bytes(), &prepared); err != nil {
		t.Fatal(err)
	}
	if prepared.State != "prepared" || prepared.Task.AvailabilityZone != "ap-northeast-2a" {
		t.Fatalf("prepared report = %+v", prepared)
	}

	imagePath := "/i/" + sourceHash + "/width=640,height=640,fit=cover,quality=80.webp"
	missingTrial := httptest.NewRecorder()
	handler.ServeHTTP(missingTrial, httptest.NewRequest(http.MethodGet, imagePath, nil))
	if missingTrial.Code != http.StatusBadRequest || missingTrial.Header().Get(e1awss4.TaskIDHeader) != "task-001" {
		t.Fatalf("missing trial response = %d, headers=%v", missingTrial.Code, missingTrial.Header())
	}

	imageResponse := httptest.NewRecorder()
	imageRequest := httptest.NewRequest(http.MethodGet, imagePath, nil)
	imageRequest.Header.Set(e1awss4.TrialIDHeader, "trial-001")
	handler.ServeHTTP(imageResponse, imageRequest)
	if imageResponse.Code != http.StatusOK || imageResponse.Body.String() != "webp-result" {
		t.Fatalf("image response = %d, %q", imageResponse.Code, imageResponse.Body.String())
	}
	assertExperimentHeaders(t, imageResponse, "trial-001")

	finish := httptest.NewRecorder()
	handler.ServeHTTP(finish, httptest.NewRequest(http.MethodPost, "/internal/e1/trials/trial-001/finish", nil))
	if finish.Code != http.StatusOK {
		t.Fatalf("finish status = %d, body = %s", finish.Code, finish.Body.String())
	}
	assertExperimentHeaders(t, finish, "trial-001")
	var report e1awss4.TrialReport
	if err := json.Unmarshal(finish.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.State != "finished" || report.Counters.ImageRequests != 1 || report.Counters.TransformAttempts != 1 || report.Counters.TransformSuccess != 1 {
		t.Fatalf("finished report = %+v", report)
	}
	if report.Counters.RequestsInflight != 0 || report.Counters.TransformInflight != 0 || report.Counters.CoordinatorKeys != 0 || report.Counters.CoordinatorWaiters != 0 {
		t.Fatalf("finished report is not drained: %+v", report.Counters)
	}

	repeatedFinish := httptest.NewRecorder()
	handler.ServeHTTP(repeatedFinish, httptest.NewRequest(http.MethodPost, "/internal/e1/trials/trial-001/finish", nil))
	if repeatedFinish.Code != http.StatusOK {
		t.Fatalf("repeated finish status = %d, body = %s", repeatedFinish.Code, repeatedFinish.Body.String())
	}
}

func assertExperimentHeaders(t *testing.T, recorder *httptest.ResponseRecorder, trialID string) {
	t.Helper()
	if got := recorder.Header().Get(e1awss4.TaskIDHeader); got != "task-001" {
		t.Fatalf("%s = %q, want task-001", e1awss4.TaskIDHeader, got)
	}
	if got := recorder.Header().Get(e1awss4.TrialIDHeader); got != trialID {
		t.Fatalf("%s = %q, want %q", e1awss4.TrialIDHeader, got, trialID)
	}
}

type increasingResourceSource struct {
	samples atomic.Uint64
}

func (s *increasingResourceSource) Sample(context.Context) (e1awss4.ResourcePoint, error) {
	sample := s.samples.Add(1)
	return e1awss4.ResourcePoint{CPUUsageNanos: sample * 100, MemoryUsageBytes: sample * 1_000}, nil
}

func newProcessor(t *testing.T) (*media.Processor, *media.DeterministicTransformer) {
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
	transformer := &media.DeterministicTransformer{Delay: time.Millisecond, Output: []byte("webp-result")}
	return media.NewProcessor(
		media.NewFileOriginalStore(map[string]string{sourceHash: originalPath}),
		derivatives,
		transformer,
		media.NewNoneCoordinator(time.Second),
		&media.Metrics{},
	), transformer
}
