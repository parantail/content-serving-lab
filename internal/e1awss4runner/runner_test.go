package e1awss4runner

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parantail/content-serving-lab/internal/e1awss4"
	"github.com/parantail/content-serving-lab/internal/media"
)

const (
	testSourceHash      = "de206136ed0eeaa42499840f49fa35bd3d57745b8a9580b16f0d6e6393ac7f91"
	testContainerDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
)

func TestBuildScheduleAlternatesScenarioOrder(t *testing.T) {
	config := testConfig(t.TempDir(), 2)
	schedule := buildSchedule(config)
	want := []string{ScenarioTask1, ScenarioTask2, ScenarioTask4, ScenarioTask4, ScenarioTask2, ScenarioTask1}
	if len(schedule) != len(want) {
		t.Fatalf("schedule length = %d, want %d", len(schedule), len(want))
	}
	for index, scenario := range want {
		if schedule[index].Scenario != scenario || schedule[index].Repetition != index/3+1 {
			t.Fatalf("schedule[%d] = %#v, want scenario %s repetition %d", index, schedule[index], scenario, index/3+1)
		}
	}
}

func TestValidateConfigRequiresCalibratedSkewAndMatchingKey(t *testing.T) {
	config := testConfig(t.TempDir(), 1)
	config.StartSkewLimit = 0
	if err := validateConfig(config); err == nil || !strings.Contains(err.Error(), "start skew") {
		t.Fatalf("validateConfig error = %v, want start skew rejection", err)
	}
	config.Calibration = true
	if err := validateConfig(config); err != nil {
		t.Fatalf("calibration with zero start skew: %v", err)
	}
	config.DerivativeKey = strings.Repeat("a", 64)
	if err := validateConfig(config); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("validateConfig error = %v, want derivative key rejection", err)
	}
}

func TestValidWebPRequiresDecodableImage(t *testing.T) {
	if !validWebP(testWebP()) {
		t.Fatal("known WebP was rejected")
	}
	structuralOnly := []byte("RIFF\x0c\x00\x00\x00WEBPVP8X\x00\x00\x00\x00")
	if validWebP(structuralOnly) {
		t.Fatal("structurally plausible but undecodable WebP was accepted")
	}
}

func TestExecuteAndAnalyzeRemoteTaskBoundary(t *testing.T) {
	root := t.TempDir()
	config := testConfig(root, 1)
	remote := newFakeRemote(config)
	storage := &fakeStorage{derivative: testWebP()}
	output, analysis, err := Execute(context.Background(), config, Dependencies{Storage: storage, Probe: remote, HTTPClient: remote})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Trials) != 3 || analysis.ValidTrials != 3 || analysis.InvalidTrials != 0 {
		t.Fatalf("trials=%d valid=%d invalid=%d", len(output.Trials), analysis.ValidTrials, analysis.InvalidTrials)
	}
	for _, trial := range output.Trials {
		if !trial.Valid || trial.TransformAttempts != int64(trial.ExpectedTasks) || trial.PublishCreated != 1 || trial.PublishExisting != int64(trial.ExpectedTasks-1) {
			t.Fatalf("unexpected trial summary: %#v", trial)
		}
	}

	required := []string{
		"run.json", "infrastructure.json", "trials.csv", "requests.csv", "tasks.csv", "resources.csv", "storage.csv", "events.csv", "cost.json",
		"analysis/analysis.json", "analysis/summary.csv", "analysis/task-distribution.svg", "analysis/duplicate-work.svg", "analysis/latency-cost.svg",
	}
	for _, name := range required {
		if _, err := os.Stat(filepath.Join(output.Directory, filepath.FromSlash(name))); err != nil {
			t.Errorf("required artifact %s: %v", name, err)
		}
		if !storage.uploaded[config.ResultPrefix+"/"+config.RunID+"/"+name] {
			t.Errorf("artifact %s was not uploaded", name)
		}
	}

	for _, name := range required[:9] {
		data, err := os.ReadFile(filepath.Join(output.Directory, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"123456789012", "private-results-bucket", "private-derivative-bucket", "one.internal", "arn:aws"} {
			if bytes.Contains(data, []byte(secret)) {
				t.Errorf("raw artifact %s contains private value %q", name, secret)
			}
		}
	}

	if _, err := Analyze(output.Directory); err != nil {
		t.Fatalf("round-trip analyze: %v", err)
	}

	tests := []struct {
		name string
		file string
		edit func(*testing.T, [][]string)
		want string
	}{
		{name: "missing request", file: "requests.csv", edit: func(t *testing.T, rows [][]string) { rows[len(rows)-1] = nil }, want: "request rows"},
		{name: "task counter", file: "tasks.csv", edit: setCSVValue("transform_success", "99"), want: "counter equations"},
		{name: "missing recheck", file: "tasks.csv", edit: setCSVValue("derivative_get_miss", "100"), want: "derivative GET equations"},
		{name: "resource sample", file: "resources.csv", edit: setCSVValue("memory_usage_bytes", "999999999"), want: "resource counters"},
		{name: "request task", file: "requests.csv", edit: setCSVValue("task_id", "unknown-task"), want: "task or AZ"},
	}
	for _, test := range tests {
		t.Run("tamper_"+test.name, func(t *testing.T) {
			tampered := filepath.Join(t.TempDir(), "run")
			copyTree(t, output.Directory, tampered)
			path := filepath.Join(tampered, test.file)
			rows := loadCSV(t, path)
			test.edit(t, rows)
			var kept [][]string
			for _, row := range rows {
				if row != nil {
					kept = append(kept, row)
				}
			}
			storeCSV(t, path, kept)
			_, err := Analyze(tampered)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Analyze error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestAnalysisFailurePreservesRawInResultStorage(t *testing.T) {
	config := testConfig(t.TempDir(), 1)
	remote := newFakeRemote(config)
	remote.corruptCounters = true
	storage := &fakeStorage{derivative: testWebP()}
	_, _, err := Execute(context.Background(), config, Dependencies{Storage: storage, Probe: remote, HTTPClient: remote})
	if err == nil || !strings.Contains(err.Error(), "derivative GET equations") {
		t.Fatalf("Execute error = %v, want analysis rejection", err)
	}
	for _, name := range []string{"run.json", "tasks.csv", "requests.csv", "resources.csv", "trials.csv"} {
		if !storage.uploaded[config.ResultPrefix+"/"+config.RunID+"/"+name] {
			t.Errorf("failed run did not preserve %s", name)
		}
	}
	if storage.uploaded[config.ResultPrefix+"/"+config.RunID+"/analysis/analysis.json"] {
		t.Fatal("failed analysis must not publish a success document")
	}
}

func TestZeroCPUWithTransformIsInvalid(t *testing.T) {
	config := testConfig(t.TempDir(), 1)
	remote := newFakeRemote(config)
	remote.zeroCPU = true
	output, analysis, err := Execute(context.Background(), config, Dependencies{Storage: &fakeStorage{derivative: testWebP()}, Probe: remote, HTTPClient: remote})
	if err != nil {
		t.Fatal(err)
	}
	if analysis.ValidTrials != 0 || analysis.InvalidTrials != 3 {
		t.Fatalf("analysis = %+v", analysis)
	}
	for _, trial := range output.Trials {
		if !strings.Contains(trial.InvalidReason, "resource_cpu_did_not_advance") {
			t.Fatalf("missing CPU quality gate: %+v", trial)
		}
	}
}

func testConfig(root string, repetitions int) Config {
	spec, err := media.ParseTransformSpec("width=640,height=640,fit=cover,quality=80", media.FormatWebP)
	if err != nil {
		panic(err)
	}
	key, err := media.DerivativeKey(testSourceHash, spec)
	if err != nil {
		panic(err)
	}
	return Config{
		RunID: "test-run", ResultsRoot: root, Region: "ap-northeast-2", GitCommit: strings.Repeat("2", 40),
		ContainerDigest: testContainerDigest, Repetitions: repetitions, SourceHash: testSourceHash,
		CanonicalSpec: spec.Canonical(), DerivativeKey: key, DerivativeBucket: "private-derivative-bucket",
		ResultBucket: "private-results-bucket", ResultPrefix: "test-results", RequestTimeout: 5 * time.Second,
		ControlTimeout: 5 * time.Second, ControlPollGap: time.Microsecond, StartSkewLimit: 5 * time.Second,
		Services: []ServiceTarget{
			{Scenario: ScenarioTask1, Endpoint: "http://one.internal", Cluster: "arn:aws:ecs:ap-northeast-2:123456789012:cluster/private", Service: "service-1", TargetGroupARN: "arn:aws:elasticloadbalancing:ap-northeast-2:123456789012:targetgroup/private-1/abc", ListenerPort: 8081, ExpectedTasks: 1},
			{Scenario: ScenarioTask2, Endpoint: "http://two.internal", Cluster: "arn:aws:ecs:ap-northeast-2:123456789012:cluster/private", Service: "service-2", TargetGroupARN: "arn:aws:elasticloadbalancing:ap-northeast-2:123456789012:targetgroup/private-2/def", ListenerPort: 8082, ExpectedTasks: 2},
			{Scenario: ScenarioTask4, Endpoint: "http://four.internal", Cluster: "arn:aws:ecs:ap-northeast-2:123456789012:cluster/private", Service: "service-4", TargetGroupARN: "arn:aws:elasticloadbalancing:ap-northeast-2:123456789012:targetgroup/private-4/ghi", ListenerPort: 8084, ExpectedTasks: 4},
		},
	}
}

type fakeStorage struct {
	mu         sync.Mutex
	derivative []byte
	exists     bool
	uploaded   map[string]bool
}

func (s *fakeStorage) DeleteDerivative(context.Context, string, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.exists = false
	return nil
}

func (s *fakeStorage) DerivativeExists(context.Context, string, string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exists, nil
}

func (s *fakeStorage) ReadDerivative(context.Context, string, string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.exists = true
	return append([]byte(nil), s.derivative...), nil
}

func (s *fakeStorage) PutResult(_ context.Context, _, key string, _ []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.uploaded == nil {
		s.uploaded = make(map[string]bool)
	}
	s.uploaded[key] = true
	return nil
}

type fakeRemote struct {
	zeroCPU         bool
	corruptCounters bool
	mu              sync.Mutex
	config          Config
	tasks           map[string][]e1awss4.TaskIdentity
	controlRR       map[string]int
	requestRR       map[string]int
	counts          map[string]map[string]int64
}

func newFakeRemote(config Config) *fakeRemote {
	remote := &fakeRemote{config: config, tasks: make(map[string][]e1awss4.TaskIdentity), controlRR: make(map[string]int), requestRR: make(map[string]int), counts: make(map[string]map[string]int64)}
	for _, target := range config.Services {
		for index := range target.ExpectedTasks {
			remote.tasks[target.Scenario] = append(remote.tasks[target.Scenario], e1awss4.TaskIdentity{
				ResourceSource: "cgroup-v2-container-visible",
				TaskID:         fmt.Sprintf("task-%d-%d", target.ExpectedTasks, index+1), TaskDefinitionFamily: "e1-media",
				TaskDefinitionRevision: "7", AvailabilityZone: fmt.Sprintf("ap-northeast-2%c", 'a'+rune(index%2)),
				LaunchType: "FARGATE", CPUVCpu: 1, MemoryMiB: 2048, ImageDigest: config.ContainerDigest,
			})
		}
	}
	return remote
}

func (r *fakeRemote) Snapshot(_ context.Context, target ServiceTarget) (HealthSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0, len(r.tasks[target.Scenario]))
	for _, task := range r.tasks[target.Scenario] {
		ids = append(ids, task.TaskID)
	}
	sort.Strings(ids)
	return HealthSnapshot{Stable: true, DesiredTasks: len(ids), RunningTasks: len(ids), HealthyTaskIDs: ids}, nil
}

func (r *fakeRemote) Do(request *http.Request) (*http.Response, error) {
	scenario, ok := map[string]string{"one.internal": ScenarioTask1, "two.internal": ScenarioTask2, "four.internal": ScenarioTask4}[request.URL.Host]
	if !ok {
		return nil, fmt.Errorf("unexpected host %q", request.URL.Host)
	}
	if strings.Contains(request.URL.Path, "/internal/e1/trials/") {
		return r.controlResponse(request, scenario)
	}
	return r.imageResponse(request, scenario)
}

func (r *fakeRemote) controlResponse(request *http.Request, scenario string) (*http.Response, error) {
	parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
	if len(parts) != 5 {
		return nil, fmt.Errorf("unexpected control path %q", request.URL.Path)
	}
	trialID, action := parts[3], parts[4]
	r.mu.Lock()
	key := action + "\x00" + trialID
	task := r.tasks[scenario][r.controlRR[key]%len(r.tasks[scenario])]
	r.controlRR[key]++
	count := r.counts[trialID][task.TaskID]
	r.mu.Unlock()
	now := time.Now().UTC()
	report := e1awss4.TrialReport{Schema: e1awss4.SchemaVersion, State: map[string]string{"prepare": "prepared", "finish": "finished"}[action], TrialID: trialID, Task: task, PreparedAt: now.Add(-time.Second).Format(time.RFC3339Nano)}
	if action == "finish" {
		created := int64(0)
		if task.TaskID == r.tasks[scenario][0].TaskID {
			created = 1
		}
		report.FinishedAt = now.Format(time.RFC3339Nano)
		report.FirstRequestAt = now.Add(-time.Millisecond).Format(time.RFC3339Nano)
		report.LastRequestAt = now.Format(time.RFC3339Nano)
		report.Counters = e1awss4.TrialCounters{
			ImageRequests: count, DerivativeGetMiss: count + 1, DerivativeGetHit: 1 - created, OriginalGetCount: 1, OriginalGetSuccess: 1,
			OriginalGetBytes: 1024, TransformAttempts: 1, TransformSuccess: 1, TransformDurationNanos: 1_000_000,
			TransformMaxInflight: 1, CoalescedRequests: count - 1, PublishCreated: created, PublishExisting: 1 - created,
			PublishAttemptBytes: int64(len(testWebP())), CPUUsageNanos: 100_000, PeakMemoryBytes: 2_000_000,
		}
		report.Resources = []e1awss4.ResourceSample{
			{Timestamp: now.Add(-time.Millisecond).Format(time.RFC3339Nano), CPUUsageNanos: 1_000_000, MemoryUsageBytes: 1_000_000},
			{Timestamp: now.Format(time.RFC3339Nano), CPUUsageNanos: 1_100_000, MemoryUsageBytes: 2_000_000},
		}
		if r.corruptCounters {
			report.Counters.DerivativeGetMiss++
		}
		if r.zeroCPU {
			report.Counters.CPUUsageNanos = 0
			report.Resources[1].CPUUsageNanos = report.Resources[0].CPUUsageNanos
		}
	}
	data, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}
	return response(http.StatusOK, data, map[string]string{e1awss4.TaskIDHeader: task.TaskID, e1awss4.TrialIDHeader: trialID}), nil
}

func (r *fakeRemote) imageResponse(request *http.Request, scenario string) (*http.Response, error) {
	transform := request.URL.Path[strings.LastIndexByte(request.URL.Path, '/')+1:]
	rawSpec, format, ok := strings.Cut(transform, ".")
	if !ok {
		return nil, fmt.Errorf("image URL has no format: %q", request.URL.Path)
	}
	if _, err := media.ParseTransformSpec(rawSpec, format); err != nil {
		return nil, fmt.Errorf("image URL transform is invalid: %w", err)
	}
	trialID := request.Header.Get(e1awss4.TrialIDHeader)
	r.mu.Lock()
	tasks := r.tasks[scenario]
	task := tasks[r.requestRR[trialID]%len(tasks)]
	r.requestRR[trialID]++
	if r.counts[trialID] == nil {
		r.counts[trialID] = make(map[string]int64)
	}
	r.counts[trialID][task.TaskID]++
	count := r.counts[trialID][task.TaskID]
	r.mu.Unlock()
	headers := map[string]string{
		e1awss4.TaskIDHeader: task.TaskID, e1awss4.TrialIDHeader: trialID,
		"X-Derivative-Key": r.config.DerivativeKey, "X-Media-Cache": "miss",
		"X-Request-Coalesced": fmt.Sprint(count > 1),
	}
	return response(http.StatusOK, testWebP(), headers), nil
}

func response(status int, body []byte, headers map[string]string) *http.Response {
	header := make(http.Header)
	for name, value := range headers {
		header.Set(name, value)
	}
	if status == http.StatusOK && header.Get("Content-Type") == "" {
		header.Set("Content-Type", "image/webp")
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(bytes.NewReader(body))}
}

func testWebP() []byte {
	data, err := base64.StdEncoding.DecodeString("UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA==")
	if err != nil {
		panic(err)
	}
	return data
}

func setCSVValue(column, value string) func(*testing.T, [][]string) {
	return func(t *testing.T, rows [][]string) {
		t.Helper()
		index := -1
		for candidate, name := range rows[0] {
			if name == column {
				index = candidate
			}
		}
		if index < 0 || len(rows) < 2 {
			t.Fatalf("column %s or data row not found", column)
		}
		rows[1][index] = value
	}
}

func loadCSV(t *testing.T, path string) [][]string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	rows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func storeCSV(t *testing.T, path string, rows [][]string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := csv.NewWriter(file)
	if err := writer.WriteAll(rows); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func copyTree(t *testing.T, source, destination string) {
	t.Helper()
	if err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	}); err != nil {
		t.Fatal(err)
	}
}
