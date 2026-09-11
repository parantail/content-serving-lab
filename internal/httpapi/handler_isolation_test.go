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

	"github.com/parantail/content-serving-lab/internal/e3control"
	"github.com/parantail/content-serving-lab/internal/httpapi"
	"github.com/parantail/content-serving-lab/internal/media"
)

const poisonedSourceHash = "abababababababababababababababababababababababababababababababab"

type blockingTransformer struct {
	calls   atomic.Int64
	started chan struct{}
	release chan struct{}
}

func (t *blockingTransformer) Transform(ctx context.Context, _ []byte, _ media.TransformSpec) ([]byte, error) {
	t.calls.Add(1)
	t.started <- struct{}{}
	select {
	case <-t.release:
		return []byte("blocking-result"), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (*blockingTransformer) Version() string { return "blocking-v1" }

func newIsolationProcessor(t *testing.T, mode media.IsolationMode, concurrency int, waitLimit time.Duration, transformer media.Transformer, withFaults bool) *media.Processor {
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
	metrics := &media.Metrics{}
	isolation, err := media.NewIsolation(mode, concurrency, waitLimit, metrics)
	if err != nil {
		t.Fatal(err)
	}
	if withFaults {
		isolation.Faults, err = media.NewFaultInjector(poisonedSourceHash, metrics)
		if err != nil {
			t.Fatal(err)
		}
	}
	processor, err := media.NewProcessorWithIsolation(
		media.NewFileOriginalStore(map[string]string{sourceHash: originalPath, poisonedSourceHash: originalPath}),
		derivatives,
		transformer,
		media.NewProcessCoordinator(5*time.Second),
		metrics,
		isolation,
	)
	if err != nil {
		t.Fatal(err)
	}
	return processor
}

func derivativePath(source string, width int) string {
	return "/i/" + source + "/width=" + itoa(width) + ",height=640,fit=cover,quality=80.webp"
}

func itoa(value int) string {
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	if digits == "" {
		return "0"
	}
	return digits
}

func postJSON(handler http.Handler, path, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)
	return recorder
}

func decodeState(t *testing.T, recorder *httptest.ResponseRecorder) e3control.State {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var state e3control.State
	if err := json.Unmarshal(recorder.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode state: %v\n%s", err, recorder.Body.String())
	}
	return state
}

func TestIsolationEndpointsAreDisabledByDefault(t *testing.T) {
	t.Parallel()

	processor := newIsolationProcessor(t, media.IsolationKillSwitch, 2, 0, &media.DeterministicTransformer{Output: []byte("webp")}, true)
	handler := httpapi.NewHandler(processor)
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/internal/e3/state", nil),
		httptest.NewRequest(http.MethodPost, "/internal/e3/fault", strings.NewReader(`{"fault":"none"}`)),
		httptest.NewRequest(http.MethodPost, "/internal/e3/kill-switch", strings.NewReader(`{"enabled":true}`)),
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s %s status = %d, want 404", request.Method, request.URL.Path, recorder.Code)
		}
	}
}

func TestShedRequestReturns503WithRetryAfter(t *testing.T) {
	t.Parallel()

	transformer := &blockingTransformer{started: make(chan struct{}, 8), release: make(chan struct{})}
	processor := newIsolationProcessor(t, media.IsolationBoundedWait, 1, 30*time.Millisecond, transformer, false)
	handler := httpapi.NewHandler(processor)

	leader := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, derivativePath(sourceHash, 1), nil))
		leader <- recorder
	}()
	<-transformer.started

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, derivativePath(sourceHash, 2), nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Retry-After"); got != httpapi.IsolationRetryAfterSeconds {
		t.Fatalf("Retry-After = %q", got)
	}
	if got := recorder.Header().Get(httpapi.IsolationHeader); got != media.IsolationOutcomeShed {
		t.Fatalf("%s = %q, want shed", httpapi.IsolationHeader, got)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := recorder.Header().Get("X-Media-Cache"); got != "miss" {
		t.Fatalf("X-Media-Cache = %q, want miss", got)
	}

	close(transformer.release)
	if leaderRecorder := <-leader; leaderRecorder.Code != http.StatusOK {
		t.Fatalf("leader status = %d, body = %s", leaderRecorder.Code, leaderRecorder.Body.String())
	}

	metricsRecorder := httptest.NewRecorder()
	handler.ServeHTTP(metricsRecorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, expected := range []string{
		"media_transform_shed_total 1",
		"media_transform_wait_seconds_count 2",
		"media_kill_switch_state 0",
		"media_original_bytes_inflight 0",
		`media_fault_injections_total{fault="slow-transform"} 0`,
	} {
		if !strings.Contains(metricsRecorder.Body.String(), expected) {
			t.Errorf("metrics missing %q:\n%s", expected, metricsRecorder.Body.String())
		}
	}
}

func TestIsolationControlFlow(t *testing.T) {
	t.Parallel()

	processor := newIsolationProcessor(t, media.IsolationKillSwitch, 2, 0, &media.DeterministicTransformer{Delay: time.Millisecond, Output: []byte("webp")}, true)
	controller, err := e3control.NewController(processor)
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.NewHandlerWithControls(processor, nil, controller)

	stateRecorder := httptest.NewRecorder()
	handler.ServeHTTP(stateRecorder, httptest.NewRequest(http.MethodGet, "/internal/e3/state", nil))
	state := decodeState(t, stateRecorder)
	if state.Mode != "kill-switch" || state.Fault == nil || state.Fault.Kind != "none" || state.KillSwitch == nil || state.KillSwitch.Enabled {
		t.Fatalf("initial state = %+v", state)
	}
	if got := stateRecorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("state Cache-Control = %q", got)
	}

	state = decodeState(t, postJSON(handler, "/internal/e3/fault", `{"fault":"slow-transform","delay":"5ms"}`))
	if state.Fault.Kind != "slow-transform" || state.Fault.Delay != "5ms" {
		t.Fatalf("fault state = %+v", state.Fault)
	}
	for _, body := range []string{
		`{"fault":"bogus"}`,
		`{"fault":"slow-transform"}`,
		`{"fault":"slow-transform","delay":"5ms","extra":1}`,
		`not json`,
	} {
		if recorder := postJSON(handler, "/internal/e3/fault", body); recorder.Code != http.StatusBadRequest {
			t.Fatalf("fault body %s status = %d, want 400", body, recorder.Code)
		}
	}

	state = decodeState(t, postJSON(handler, "/internal/e3/kill-switch", `{"enabled":true}`))
	if !state.KillSwitch.Enabled || len(state.KillSwitch.Transitions) != 1 {
		t.Fatalf("kill switch state = %+v", state.KillSwitch)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, derivativePath(sourceHash, 1), nil))
	if recorder.Code != http.StatusServiceUnavailable || recorder.Header().Get(httpapi.IsolationHeader) != media.IsolationOutcomeKillSwitch {
		t.Fatalf("miss during kill switch: status = %d, header = %q", recorder.Code, recorder.Header().Get(httpapi.IsolationHeader))
	}
	if got := recorder.Header().Get("Retry-After"); got != httpapi.IsolationRetryAfterSeconds {
		t.Fatalf("Retry-After = %q", got)
	}

	decodeState(t, postJSON(handler, "/internal/e3/kill-switch", `{"enabled":false}`))
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, derivativePath(sourceHash, 1), nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("miss after disabling: status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get(httpapi.IsolationHeader); got != "" {
		t.Fatalf("%s = %q on success, want empty", httpapi.IsolationHeader, got)
	}
}

func TestIsolationControlsRejectUnavailableActions(t *testing.T) {
	t.Parallel()

	processor := newIsolationProcessor(t, media.IsolationBaseline, 2, 0, &media.DeterministicTransformer{Output: []byte("webp")}, false)
	controller, err := e3control.NewController(processor)
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.NewHandlerWithControls(processor, nil, controller)

	if recorder := postJSON(handler, "/internal/e3/kill-switch", `{"enabled":true}`); recorder.Code != http.StatusConflict {
		t.Fatalf("kill switch in baseline status = %d, want 409", recorder.Code)
	}
	if recorder := postJSON(handler, "/internal/e3/fault", `{"fault":"transform-error"}`); recorder.Code != http.StatusConflict {
		t.Fatalf("fault without injector status = %d, want 409", recorder.Code)
	}
	state := decodeState(t, func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/internal/e3/state", nil))
		return recorder
	}())
	if state.Fault != nil || state.KillSwitch != nil || state.Mode != "baseline" {
		t.Fatalf("baseline state = %+v", state)
	}
}
