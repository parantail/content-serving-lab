package e3runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/parantail/content-serving-lab/internal/e3control"
	"github.com/parantail/content-serving-lab/internal/httpapi"
	"github.com/parantail/content-serving-lab/internal/media"
)

type RunOutput struct {
	Directory string
	Metadata  RunMetadata
	Trials    []TrialResult
}

type scheduledTrial struct {
	ID         string
	Mode       media.IsolationMode
	Fault      media.FaultKind
	Repetition int
}

type trialMetrics struct {
	TrialID  string
	Mode     string
	Fault    string
	Snapshot media.MetricsSnapshot
}

type runner struct {
	config       Config
	fixture      []byte
	sourceHash   string
	poisonedHash string
	hitSpecs     []media.TransformSpec
	hostname     string
	s3Client     *s3.Client

	specMu      sync.Mutex
	specCounter int
}

func Execute(config Config) (RunOutput, error) {
	if err := validateConfig(config); err != nil {
		return RunOutput{}, err
	}
	fixture, err := os.ReadFile(config.FixturePath)
	if err != nil {
		return RunOutput{}, fmt.Errorf("read fixture: %w", err)
	}
	fixtureDigest := sha256.Sum256(fixture)
	fixtureHash := hex.EncodeToString(fixtureDigest[:])
	sourceHash := fixtureHash
	if config.Storage == StorageS3 {
		// A shared bucket keeps derivatives across runs, so each run addresses
		// the fixture through a run-scoped alias and never sees stale hits.
		sourceHash = RunScopedSourceHash(config.RunID, fixtureHash)
	}
	poisonedHash := PoisonedSourceHash(sourceHash)
	hitSpecs, err := HitSpecs(config.HitKeys)
	if err != nil {
		return RunOutput{}, err
	}
	hostname, _ := os.Hostname()

	r := &runner{
		config:       config,
		fixture:      fixture,
		sourceHash:   sourceHash,
		poisonedHash: poisonedHash,
		hitSpecs:     hitSpecs,
		hostname:     hostname,
	}
	if config.Storage == StorageS3 {
		if err := r.prepareS3(); err != nil {
			return RunOutput{}, err
		}
	}

	metadata := NewRunMetadata(config)
	metadata.Hostname = hostname
	metadata.Transformer = media.VipsTransformerVersion()
	if config.transformer != nil {
		metadata.Transformer = config.transformer.Version()
	}
	metadata.FixturePath = filepath.Base(config.FixturePath)
	metadata.FixtureSHA256 = fixtureHash
	metadata.FixtureBytes = len(fixture)
	metadata.SourceHash = sourceHash
	metadata.PoisonedSourceHash = poisonedHash
	for _, spec := range hitSpecs {
		metadata.HitSpecs = append(metadata.HitSpecs, spec.Canonical())
	}
	metadata.CPUQuota = readCPUQuota()
	metadata.MemoryLimitBytes = readMemoryLimit()
	metadata.Commands = []string{strings.Join(config.Command, " ")}
	metadata.Environment["coordinator_mode"] = media.CoordinatorProcessSingle
	metadata.Environment["libvips_concurrency"] = "1"
	metadata.Environment["libvips_operation_cache"] = "disabled"
	metadata.Environment["transform_timeout_semantics"] = "process-singleflight work context bounds slot wait, original read and transform together"
	metadata.KnownLimitations = []string{
		"The three request streams are synthetic loopback HTTP traffic generated inside the same container as the service, so the generator shares the CPU quota with the service.",
		"Trials run sequentially in one process; native allocator state can carry across trials, so peak memory is comparable between trials only as a trend.",
		"The kill switch is toggled at fixed offsets after fault start and end; no automatic detection is measured.",
		"The generator never retries, so retry amplification is out of scope.",
	}

	output := RunOutput{
		Directory: filepath.Join(config.ResultsRoot, config.RunID),
		Metadata:  metadata,
	}
	if _, err := os.Stat(output.Directory); err == nil {
		return RunOutput{}, fmt.Errorf("result directory %s already exists; refusing to overwrite", output.Directory)
	}
	if err := os.MkdirAll(output.Directory, 0o755); err != nil {
		return RunOutput{}, fmt.Errorf("create result directory: %w", err)
	}
	writer, err := openRawWriter(output.Directory)
	if err != nil {
		return RunOutput{}, err
	}
	defer writer.close()
	var metrics []trialMetrics
	if err := writeRunSummary(output, metrics); err != nil {
		return RunOutput{}, err
	}

	for _, item := range buildSchedule(config) {
		output.Metadata.ExecutionOrder = append(output.Metadata.ExecutionOrder, item.ID)
		fmt.Fprintf(os.Stderr, "trial_started=%s\n", item.ID)
		result, err := r.executeTrial(item, writer)
		if err != nil {
			return RunOutput{}, fmt.Errorf("execute %s: %w", item.ID, err)
		}
		output.Trials = append(output.Trials, result.trial)
		metrics = append(metrics, trialMetrics{TrialID: item.ID, Mode: string(item.Mode), Fault: string(item.Fault), Snapshot: result.delta})
		if err := writeRunSummary(output, metrics); err != nil {
			return RunOutput{}, err
		}
		hit := result.trial.Summaries[StreamHit][PhaseFault]
		miss := result.trial.Summaries[StreamHealthyMiss][PhaseFault]
		fmt.Fprintf(os.Stderr, "trial_finished=%s valid=%t reason=%q fault_hit_err=%d/%d fault_miss_err=%d/%d fault_miss_p99_ms=%.1f shed=%d kill=%d\n",
			item.ID, result.trial.Valid, result.trial.InvalidReason, hit.Errors, hit.Requests, miss.Errors, miss.Requests, miss.P99MS, result.trial.ShedRequests, result.trial.KillSwitchRequests)
	}
	return output, nil
}

// RunScopedSourceHash derives the per-run alias under which S3 runs upload
// the fixture. It is a valid SHA-256 hex string distinct from the fixture hash.
func RunScopedSourceHash(runID, fixtureHash string) string {
	digest := sha256.Sum256([]byte("e3-run\n" + runID + "\n" + strings.ToLower(fixtureHash)))
	return hex.EncodeToString(digest[:])
}

// PoisonedSourceHash derives the poisoned alias for a fixture hash. The
// alias is a valid SHA-256 hex string that never equals the real hash.
func PoisonedSourceHash(sourceHash string) string {
	digest := sha256.Sum256([]byte("e3-poisoned\n" + strings.ToLower(sourceHash)))
	return hex.EncodeToString(digest[:])
}

// HitSpecs returns the fixed derivative specs of the hit stream. They use a
// 480px height so they can never collide with generated miss specs.
func HitSpecs(count int) ([]media.TransformSpec, error) {
	specs := make([]media.TransformSpec, 0, count)
	for index := range count {
		spec, err := media.ParseTransformSpec(fmt.Sprintf("width=%d,height=480,fit=cover,quality=80", 640+index), "webp")
		if err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

// nextMissSpec enumerates unique specs with near-identical transform cost:
// width and height in 600..680 and quality in 70..90.
func (r *runner) nextMissSpec() media.TransformSpec {
	r.specMu.Lock()
	n := r.specCounter
	r.specCounter++
	r.specMu.Unlock()
	return MissSpec(n)
}

func MissSpec(n int) media.TransformSpec {
	return media.TransformSpec{
		Width:   600 + n%81,
		Height:  600 + (n/81)%81,
		Fit:     media.FitCover,
		Format:  media.FormatWebP,
		Quality: 70 + (n/(81*81))%21,
	}
}

func buildSchedule(config Config) []scheduledTrial {
	var schedule []scheduledTrial
	for repetition := 1; repetition <= config.Repetitions; repetition++ {
		for faultIndex, fault := range config.Faults {
			for modeOffset := range config.Modes {
				mode := config.Modes[(modeOffset+repetition+faultIndex)%len(config.Modes)]
				schedule = append(schedule, scheduledTrial{
					ID:         fmt.Sprintf("%s-%s-r%02d", mode, fault, repetition),
					Mode:       mode,
					Fault:      fault,
					Repetition: repetition,
				})
			}
		}
	}
	return schedule
}

func loadS3Client(ctx context.Context) (*s3.Client, error) {
	configuration, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}
	return s3.NewFromConfig(configuration), nil
}

func (r *runner) prepareS3() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := loadS3Client(ctx)
	if err != nil {
		return err
	}
	r.s3Client = client
	originals, err := media.NewS3OriginalStore(r.s3Client, r.config.OriginalBucket)
	if err != nil {
		return err
	}
	for _, hash := range []string{r.sourceHash, r.poisonedHash} {
		data, err := originals.Read(ctx, hash)
		if err == nil {
			if !bytes.Equal(data, r.fixture) {
				return fmt.Errorf("original %s already exists with different content", hash)
			}
			continue
		}
		if !errors.Is(err, media.ErrOriginalNotFound) {
			return fmt.Errorf("check original %s: %w", hash, err)
		}
		_, err = r.s3Client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:      aws.String(r.config.OriginalBucket),
			Key:         aws.String(media.S3OriginalObjectPrefix + hash),
			Body:        bytes.NewReader(r.fixture),
			ContentType: aws.String("image/jpeg"),
			IfNoneMatch: aws.String("*"),
		})
		if err != nil {
			return fmt.Errorf("upload original %s: %w", hash, err)
		}
	}
	return nil
}

type trialOutput struct {
	trial TrialResult
	delta media.MetricsSnapshot
}

type trialRun struct {
	r          *runner
	item       scheduledTrial
	client     *http.Client
	serverURL  string
	controller *e3control.Controller
	start      time.Time

	mu       sync.Mutex
	requests []RequestResult
	events   []Event
	states   []StateSample
}

func (r *runner) executeTrial(item scheduledTrial, writer *rawWriter) (trialOutput, error) {
	config := r.config
	originals, derivatives, cleanup, err := r.newStores(item.ID)
	if err != nil {
		return trialOutput{}, err
	}
	defer cleanup()

	metrics := &media.Metrics{}
	isolation, err := media.NewIsolation(item.Mode, config.TransformConcurrency, config.SlotWaitLimit, metrics)
	if err != nil {
		return trialOutput{}, err
	}
	isolation.Faults, err = media.NewFaultInjector(r.poisonedHash, metrics)
	if err != nil {
		return trialOutput{}, err
	}
	transformer := config.transformer
	if transformer == nil {
		transformer = media.NewUnboundedVipsTransformer()
	}
	processor, err := media.NewProcessorWithIsolation(originals, derivatives, transformer, media.NewProcessCoordinator(config.TransformTimeout), metrics, isolation)
	if err != nil {
		return trialOutput{}, err
	}
	controller, err := e3control.NewController(processor)
	if err != nil {
		return trialOutput{}, err
	}
	server := httptest.NewServer(httpapi.NewHandlerWithControls(processor, nil, controller))
	defer server.Close()

	transport := &http.Transport{MaxIdleConns: 512, MaxIdleConnsPerHost: 512, IdleConnTimeout: 90 * time.Second}
	t := &trialRun{
		r:          r,
		item:       item,
		client:     &http.Client{Transport: transport},
		serverURL:  server.URL,
		controller: controller,
	}
	defer transport.CloseIdleConnections()

	// Prewarm happens before the timeline starts; its rows carry negative
	// offsets relative to the stream start.
	prewarmStart := time.Now()
	t.start = prewarmStart
	if err := t.prewarm(); err != nil {
		return trialOutput{}, err
	}
	base := metrics.Snapshot()

	t.start = time.Now()
	total := config.totalDuration()
	t.record(Event{Event: "trial_started", Detail: fmt.Sprintf("mode=%s fault=%s", item.Mode, item.Fault)}, t.start)
	resources := startResourceSampler(config.RunID, item.ID, t.start, config.ResourceSampleGap)
	stopState := t.startStateSampler()

	var streams sync.WaitGroup
	var inflightRequests sync.WaitGroup
	streamSpecs := []streamSpec{
		{kind: StreamHit, rate: config.HitRate, source: r.sourceHash, next: t.nextHitSpec()},
		{kind: StreamHealthyMiss, rate: config.HealthyMissRate, source: r.sourceHash, next: r.nextMissSpec},
		{kind: StreamPoisonedMiss, rate: config.PoisonedMissRate, source: r.poisonedHash, next: r.nextMissSpec},
	}
	for _, spec := range streamSpecs {
		streams.Add(1)
		go func(spec streamSpec) {
			defer streams.Done()
			t.runStream(spec, &inflightRequests)
		}(spec)
	}
	orchestrated := make(chan struct{})
	go func() {
		defer close(orchestrated)
		t.orchestrate()
	}()
	streams.Wait()
	t.record(Event{Event: "streams_finished"}, time.Now())
	<-orchestrated
	inflightRequests.Wait()
	t.record(Event{Event: "requests_drained"}, time.Now())
	stopState()
	resourceSamples := resources.finish()
	finished := time.Now()

	delta := subtractSnapshot(metrics.Snapshot(), base)
	t.mu.Lock()
	requests := append([]RequestResult(nil), t.requests...)
	events := append([]Event(nil), t.events...)
	states := append([]StateSample(nil), t.states...)
	t.mu.Unlock()
	sort.SliceStable(requests, func(i, j int) bool { return requests[i].OffsetMS < requests[j].OffsetMS })

	trial := summarizeTrial(config, item, t.start, finished, total, requests, events, states, resourceSamples, delta)
	if err := writer.appendTrial(requests, events, states, resourceSamples); err != nil {
		return trialOutput{}, err
	}
	return trialOutput{trial: trial, delta: delta}, nil
}

func (r *runner) newStores(trialID string) (media.OriginalStore, media.DerivativeStore, func(), error) {
	if r.config.Storage == StorageS3 {
		originals, err := media.NewS3OriginalStore(r.s3Client, r.config.OriginalBucket)
		if err != nil {
			return nil, nil, nil, err
		}
		derivatives, err := media.NewS3DerivativeStore(r.s3Client, r.config.DerivativeBucket)
		if err != nil {
			return nil, nil, nil, err
		}
		return originals, derivatives, func() {}, nil
	}
	directory, err := os.MkdirTemp("", "content-serving-e3-"+trialID+"-")
	if err != nil {
		return nil, nil, nil, err
	}
	originalPath := filepath.Join(directory, "original.jpg")
	if err := os.WriteFile(originalPath, r.fixture, 0o644); err != nil {
		os.RemoveAll(directory)
		return nil, nil, nil, err
	}
	derivatives, err := media.NewLocalDerivativeStore(filepath.Join(directory, "derivatives"))
	if err != nil {
		os.RemoveAll(directory)
		return nil, nil, nil, err
	}
	originals := media.NewFileOriginalStore(map[string]string{r.sourceHash: originalPath, r.poisonedHash: originalPath})
	return originals, derivatives, func() { os.RemoveAll(directory) }, nil
}

type streamSpec struct {
	kind   string
	rate   float64
	source string
	next   func() media.TransformSpec
}

func (t *trialRun) nextHitSpec() func() media.TransformSpec {
	var counter atomic.Int64
	specs := t.r.hitSpecs
	return func() media.TransformSpec {
		index := counter.Add(1) - 1
		return specs[int(index)%len(specs)]
	}
}

func (t *trialRun) prewarm() error {
	for index, spec := range t.r.hitSpecs {
		var warmed bool
		for attempt := 1; attempt <= 3 && !warmed; attempt++ {
			row := t.issue(StreamPrewarm, fmt.Sprintf("prewarm-%02d-%d", index+1, attempt), t.r.sourceHash, spec)
			t.record(row, time.Time{})
			warmed = row.HTTPStatus == http.StatusOK
		}
		if !warmed {
			return fmt.Errorf("prewarm hit key %s failed", spec.Canonical())
		}
		check := t.issue(StreamPrewarm, fmt.Sprintf("prewarm-%02d-check", index+1), t.r.sourceHash, spec)
		t.record(check, time.Time{})
		if check.HTTPStatus != http.StatusOK || check.Cache != "derivative" {
			return fmt.Errorf("prewarm hit key %s did not become a derivative hit (status %d, cache %q)", spec.Canonical(), check.HTTPStatus, check.Cache)
		}
	}
	return nil
}

func (t *trialRun) runStream(spec streamSpec, inflightRequests *sync.WaitGroup) {
	interval := time.Duration(float64(time.Second) / spec.rate)
	deadline := t.start.Add(t.r.config.totalDuration())
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var inflight atomic.Int64
	sequence := 0
	for now := range ticker.C {
		if !now.Before(deadline) {
			return
		}
		sequence++
		requestID := fmt.Sprintf("%s-%05d", spec.kind, sequence)
		transformSpec := spec.next()
		if int(inflight.Load()) >= t.r.config.MaxInflight {
			row := t.newRow(spec.kind, requestID, spec.source, transformSpec, now)
			row.ErrorType = "generator_saturated"
			row.FinishedAt = row.StartedAt
			t.record(row, time.Time{})
			continue
		}
		inflight.Add(1)
		inflightRequests.Add(1)
		go func() {
			defer inflightRequests.Done()
			defer inflight.Add(-1)
			t.record(t.issue(spec.kind, requestID, spec.source, transformSpec), time.Time{})
		}()
	}
}

func (t *trialRun) newRow(stream, requestID, source string, spec media.TransformSpec, started time.Time) RequestResult {
	offset := milliseconds(started.Sub(t.start))
	return RequestResult{
		RunID:      t.r.config.RunID,
		TrialID:    t.item.ID,
		Mode:       string(t.item.Mode),
		Fault:      string(t.item.Fault),
		Repetition: t.item.Repetition,
		Stream:     stream,
		RequestID:  requestID,
		SourceHash: source,
		Spec:       spec.Canonical(),
		StartedAt:  started.UTC().Format(time.RFC3339Nano),
		OffsetMS:   offset,
		Phase:      t.phaseAt(stream, offset),
	}
}

func (t *trialRun) phaseAt(stream string, offsetMS float64) string {
	if stream == StreamPrewarm {
		return PhasePrewarm
	}
	config := t.r.config
	normal := milliseconds(config.NormalDuration)
	fault := normal + milliseconds(config.FaultDuration)
	total := fault + milliseconds(config.RecoveryDuration)
	switch {
	case offsetMS < normal:
		return PhaseNormal
	case offsetMS < fault:
		return PhaseFault
	case offsetMS < total:
		return PhaseRecovery
	default:
		return PhaseDrain
	}
}

func (t *trialRun) issue(stream, requestID, source string, spec media.TransformSpec) RequestResult {
	started := time.Now()
	row := t.newRow(stream, requestID, source, spec, started)
	rawSpec := fmt.Sprintf("width=%d,height=%d,fit=%s,quality=%d", spec.Width, spec.Height, spec.Fit, spec.Quality)
	requestURL := t.serverURL + "/i/" + source + "/" + url.PathEscape(rawSpec) + "." + spec.Format
	ctx, cancel := context.WithTimeout(context.Background(), t.r.config.RequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		row.ErrorType = "request_build"
		return finishRow(row, started)
	}
	response, err := t.client.Do(request)
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			row.ErrorType = "timeout"
		case errors.Is(err, context.Canceled):
			row.ErrorType = "canceled"
		default:
			row.ErrorType = "transport"
		}
		return finishRow(row, started)
	}
	defer response.Body.Close()
	data, readErr := io.ReadAll(response.Body)
	row.HTTPStatus = response.StatusCode
	row.ImageKey = response.Header.Get("X-Derivative-Key")
	row.Cache = response.Header.Get("X-Media-Cache")
	row.Coalesced = response.Header.Get("X-Request-Coalesced") == "true"
	row.Isolation = response.Header.Get(httpapi.IsolationHeader)
	row.RetryAfter = response.Header.Get("Retry-After")
	switch {
	case readErr != nil:
		row.ErrorType = "response_read"
	case response.StatusCode != http.StatusOK:
		row.ErrorType = "http_" + strconv.Itoa(response.StatusCode)
	default:
		digest := sha256.Sum256(data)
		row.ResponseSHA256 = hex.EncodeToString(digest[:])
	}
	return finishRow(row, started)
}

func finishRow(row RequestResult, started time.Time) RequestResult {
	finished := time.Now()
	row.FinishedAt = finished.UTC().Format(time.RFC3339Nano)
	row.LatencyMS = milliseconds(finished.Sub(started))
	return row
}

func (t *trialRun) orchestrate() {
	config := t.r.config
	faultOn := t.start.Add(config.NormalDuration)
	faultOff := faultOn.Add(config.FaultDuration)
	killSwitch := t.item.Mode == media.IsolationKillSwitch && t.item.Fault != media.FaultNone

	sleepUntil(faultOn)
	t.record(Event{Event: "phase_fault_started"}, time.Now())
	if t.item.Fault != media.FaultNone {
		delay := faultDelayFor(config, t.item.Fault)
		if _, err := t.controller.SetFault(string(t.item.Fault), delay.String()); err != nil {
			t.record(Event{Event: "fault_set_failed", Detail: err.Error()}, time.Now())
		} else {
			t.record(Event{Event: "fault_on", Detail: fmt.Sprintf("kind=%s delay=%s", t.item.Fault, delay)}, time.Now())
		}
	}
	if killSwitch {
		sleepUntil(faultOn.Add(config.KillSwitchOnDelay))
		if _, err := t.controller.SetKillSwitch(true); err != nil {
			t.record(Event{Event: "kill_switch_failed", Detail: err.Error()}, time.Now())
		} else {
			t.record(Event{Event: "kill_switch_on"}, time.Now())
		}
	}
	sleepUntil(faultOff)
	t.record(Event{Event: "phase_recovery_started"}, time.Now())
	if t.item.Fault != media.FaultNone {
		if _, err := t.controller.SetFault(string(media.FaultNone), ""); err != nil {
			t.record(Event{Event: "fault_clear_failed", Detail: err.Error()}, time.Now())
		} else {
			t.record(Event{Event: "fault_off"}, time.Now())
		}
	}
	if killSwitch {
		sleepUntil(faultOff.Add(config.KillSwitchOffDelay))
		if _, err := t.controller.SetKillSwitch(false); err != nil {
			t.record(Event{Event: "kill_switch_failed", Detail: err.Error()}, time.Now())
		} else {
			t.record(Event{Event: "kill_switch_off"}, time.Now())
		}
	}
}

// FaultDelayFor returns the delay applied to a fault kind: the configured
// delay for the slow faults and zero for the others.
func faultDelayFor(config Config, fault media.FaultKind) time.Duration {
	switch fault {
	case media.FaultSlowTransform, media.FaultSlowOriginal:
		return config.FaultDelay
	default:
		return 0
	}
}

func sleepUntil(at time.Time) {
	if wait := time.Until(at); wait > 0 {
		time.Sleep(wait)
	}
}

func (t *trialRun) startStateSampler() func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t.captureState()
		ticker := time.NewTicker(t.r.config.StateSampleGap)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				t.captureState()
			case <-stop:
				t.captureState()
				return
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

func (t *trialRun) captureState() {
	now := time.Now()
	state := t.controller.State()
	sample := StateSample{
		RunID:                 t.r.config.RunID,
		TrialID:               t.item.ID,
		Timestamp:             now.UTC().Format(time.RFC3339Nano),
		OffsetMS:              milliseconds(now.Sub(t.start)),
		TransformInflight:     state.Processor.TransformInflight,
		TransformWaiting:      state.Processor.TransformWaiting,
		CoordinatorKeys:       state.Processor.CoordinatorKeys,
		CoordinatorWaiters:    state.Processor.CoordinatorWaiters,
		OriginalBytesInflight: state.Metrics.OriginalBytesInflight,
		DerivativeHits:        state.Metrics.DerivativeHits,
		DerivativeMisses:      state.Metrics.DerivativeMisses,
		TransformSuccess:      state.Metrics.TransformSuccess,
		TransformError:        state.Metrics.TransformError,
		TransformTimeout:      state.Metrics.TransformTimeout,
		TransformShed:         state.Metrics.TransformShed,
		KillSwitchRejected:    state.Metrics.KillSwitchRejected,
		FaultInjections:       state.Metrics.FaultSlowTransform + state.Metrics.FaultTransformTimeout + state.Metrics.FaultTransformError + state.Metrics.FaultSlowOriginal,
	}
	if state.Fault != nil {
		sample.FaultKind = state.Fault.Kind
	}
	if state.KillSwitch != nil {
		sample.KillSwitchEnabled = state.KillSwitch.Enabled
	}
	t.mu.Lock()
	t.states = append(t.states, sample)
	t.mu.Unlock()
}

// record stores a request row or an event. Events take the given time.
func (t *trialRun) record(value any, at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch v := value.(type) {
	case RequestResult:
		t.requests = append(t.requests, v)
	case Event:
		v.RunID = t.r.config.RunID
		v.TrialID = t.item.ID
		v.Timestamp = at.UTC().Format(time.RFC3339Nano)
		v.OffsetMS = milliseconds(at.Sub(t.start))
		t.events = append(t.events, v)
	}
}

func subtractSnapshot(after, before media.MetricsSnapshot) media.MetricsSnapshot {
	var delta media.MetricsSnapshot
	afterValue := reflect.ValueOf(after)
	beforeValue := reflect.ValueOf(before)
	deltaValue := reflect.ValueOf(&delta).Elem()
	for index := range afterValue.NumField() {
		field := deltaValue.Field(index)
		if field.Kind() == reflect.Int64 {
			field.SetInt(afterValue.Field(index).Int() - beforeValue.Field(index).Int())
		}
	}
	// Gauges are not deltas: keep the final values.
	delta.TransformInflight = after.TransformInflight
	delta.TransformMaxInflight = after.TransformMaxInflight
	delta.KillSwitchState = after.KillSwitchState
	delta.OriginalBytesInflight = after.OriginalBytesInflight
	return delta
}

func summarizeTrial(
	config Config,
	item scheduledTrial,
	started, finished time.Time,
	total time.Duration,
	requests []RequestResult,
	events []Event,
	states []StateSample,
	resources []ResourceSample,
	delta media.MetricsSnapshot,
) TrialResult {
	trial := TrialResult{
		RunID:                 config.RunID,
		TrialID:               item.ID,
		Mode:                  string(item.Mode),
		Fault:                 string(item.Fault),
		Repetition:            item.Repetition,
		Valid:                 true,
		StartedAt:             started.UTC().Format(time.RFC3339Nano),
		FinishedAt:            finished.UTC().Format(time.RFC3339Nano),
		FaultOnOffsetMS:       -1,
		FaultOffOffsetMS:      -1,
		KillSwitchOnOffsetMS:  -1,
		KillSwitchOffOffsetMS: -1,
		DerivativeHits:        delta.DerivativeHits,
		DerivativeMisses:      delta.DerivativeMisses,
		TransformSuccess:      delta.TransformSuccess,
		TransformError:        delta.TransformError,
		TransformTimeout:      delta.TransformTimeout,
		TransformShed:         delta.TransformShed,
		KillSwitchRejected:    delta.KillSwitchRejected,
		FaultInjections:       delta.FaultSlowTransform + delta.FaultTransformTimeout + delta.FaultTransformError + delta.FaultSlowOriginal,
		Summaries:             summarizeStreams(requests),
	}
	for _, row := range requests {
		if row.Stream == StreamPrewarm {
			continue
		}
		trial.TotalRequests++
		switch {
		case row.ErrorType == "generator_saturated":
			trial.SaturatedRequests++
		case row.HTTPStatus == http.StatusOK && row.ErrorType == "":
			trial.SuccessRequests++
		default:
			trial.ErrorRequests++
		}
		switch row.Isolation {
		case media.IsolationOutcomeShed:
			trial.ShedRequests++
		case media.IsolationOutcomeKillSwitch:
			trial.KillSwitchRequests++
		}
	}
	for _, event := range events {
		switch event.Event {
		case "fault_on":
			trial.FaultOnOffsetMS = event.OffsetMS
		case "fault_off":
			trial.FaultOffOffsetMS = event.OffsetMS
		case "kill_switch_on":
			trial.KillSwitchOnOffsetMS = event.OffsetMS
		case "kill_switch_off":
			trial.KillSwitchOffOffsetMS = event.OffsetMS
		}
	}
	for _, state := range states {
		trial.MaxTransformWaiting = max(trial.MaxTransformWaiting, state.TransformWaiting)
		trial.MaxOriginalBytesInflight = max(trial.MaxOriginalBytesInflight, state.OriginalBytesInflight)
	}
	if len(resources) > 0 {
		firstCPU := resources[0].CPUUsageUsec
		lastCPU := resources[len(resources)-1].CPUUsageUsec
		if firstCPU >= 0 && lastCPU >= firstCPU {
			trial.CPUTimeMS = float64(lastCPU-firstCPU) / 1000
		}
		for _, sample := range resources {
			trial.PeakRSSBytes = max(trial.PeakRSSBytes, sample.RSSBytes)
			trial.PeakCgroupMemBytes = max(trial.PeakCgroupMemBytes, sample.CgroupMemoryBytes)
		}
	}
	validateTrial(config, item, &trial, requests, total)
	return trial
}

func summarizeStreams(requests []RequestResult) map[string]map[string]StreamPhaseSummary {
	latencies := make(map[string]map[string][]float64)
	summaries := make(map[string]map[string]StreamPhaseSummary)
	add := func(stream, phase string, row RequestResult) {
		if summaries[stream] == nil {
			summaries[stream] = make(map[string]StreamPhaseSummary)
			latencies[stream] = make(map[string][]float64)
		}
		summary := summaries[stream][phase]
		summary.Requests++
		switch {
		case row.ErrorType == "generator_saturated":
			summary.Saturated++
			summaries[stream][phase] = summary
			return
		case row.HTTPStatus == http.StatusOK && row.ErrorType == "":
			summary.Success++
		default:
			summary.Errors++
		}
		if row.Isolation == media.IsolationOutcomeShed {
			summary.Shed++
		}
		if row.Isolation == media.IsolationOutcomeKillSwitch {
			summary.KillSwitch++
		}
		if row.HTTPStatus == http.StatusInternalServerError {
			summary.HTTP500++
		}
		if row.ErrorType == "timeout" {
			summary.Timeouts++
		}
		latencies[stream][phase] = append(latencies[stream][phase], row.LatencyMS)
		summaries[stream][phase] = summary
	}
	for _, row := range requests {
		add(row.Stream, row.Phase, row)
		if row.Stream != StreamPrewarm {
			add(row.Stream, "all", row)
		}
	}
	for stream, phases := range summaries {
		for phase, summary := range phases {
			values := latencies[stream][phase]
			sort.Float64s(values)
			summary.P50MS = percentile(values, 0.50)
			summary.P95MS = percentile(values, 0.95)
			summary.P99MS = percentile(values, 0.99)
			if len(values) > 0 {
				summary.MaxMS = values[len(values)-1]
				sum := 0.0
				for _, value := range values {
					sum += value
				}
				summary.MeanMS = sum / float64(len(values))
			}
			summaries[stream][phase] = summary
		}
	}
	return summaries
}

func validateTrial(config Config, item scheduledTrial, trial *TrialResult, requests []RequestResult, total time.Duration) {
	if trial.SaturatedRequests > 0 {
		invalidate(trial, "generator_saturated")
	}
	expected := map[string]float64{
		StreamHit:          config.HitRate * total.Seconds(),
		StreamHealthyMiss:  config.HealthyMissRate * total.Seconds(),
		StreamPoisonedMiss: config.PoisonedMissRate * total.Seconds(),
	}
	for stream, want := range expected {
		got := float64(trial.Summaries[stream]["all"].Requests)
		tolerance := max(3, want*0.05)
		if got < want-tolerance || got > want+tolerance {
			invalidate(trial, "stream_count_"+stream)
		}
	}
	hashesByKey := make(map[string]map[string]bool)
	for _, row := range requests {
		if row.Stream == StreamHit && row.HTTPStatus == http.StatusOK && row.Cache != "derivative" {
			invalidate(trial, "hit_stream_served_as_miss")
		}
		if row.Stream != StreamPrewarm && row.Stream != StreamHit && row.HTTPStatus == http.StatusOK && row.Cache != "miss" {
			invalidate(trial, "miss_stream_served_as_hit")
		}
		if row.Stream == StreamHit && row.ResponseSHA256 != "" {
			if hashesByKey[row.ImageKey] == nil {
				hashesByKey[row.ImageKey] = make(map[string]bool)
			}
			hashesByKey[row.ImageKey][row.ResponseSHA256] = true
		}
	}
	for _, hashes := range hashesByKey {
		if len(hashes) != 1 {
			invalidate(trial, "hit_response_hash_mismatch")
		}
	}
	if item.Fault != media.FaultNone {
		normal := milliseconds(config.NormalDuration)
		if trial.FaultOnOffsetMS < 0 || abs(trial.FaultOnOffsetMS-normal) > 1000 {
			invalidate(trial, "fault_on_timing")
		}
		if trial.FaultOffOffsetMS < 0 || abs(trial.FaultOffOffsetMS-(normal+milliseconds(config.FaultDuration))) > 1000 {
			invalidate(trial, "fault_off_timing")
		}
		if trial.FaultInjections == 0 {
			invalidate(trial, "fault_not_injected")
		}
		if item.Mode == media.IsolationKillSwitch {
			if trial.KillSwitchOnOffsetMS < 0 || abs(trial.KillSwitchOnOffsetMS-(trial.FaultOnOffsetMS+milliseconds(config.KillSwitchOnDelay))) > 1000 {
				invalidate(trial, "kill_switch_on_timing")
			}
			if trial.KillSwitchOffOffsetMS < 0 || abs(trial.KillSwitchOffOffsetMS-(trial.FaultOffOffsetMS+milliseconds(config.KillSwitchOffDelay))) > 1000 {
				invalidate(trial, "kill_switch_off_timing")
			}
		}
	} else if trial.FaultInjections != 0 || trial.FaultOnOffsetMS >= 0 || trial.KillSwitchOnOffsetMS >= 0 {
		invalidate(trial, "unexpected_fault_or_kill_switch")
	}
	if item.Mode != media.IsolationBoundedWait && trial.ShedRequests != 0 {
		invalidate(trial, "shed_outside_bounded_wait")
	}
	if item.Mode != media.IsolationKillSwitch && trial.KillSwitchRequests != 0 {
		invalidate(trial, "kill_switch_outside_mode")
	}
}

func invalidate(trial *TrialResult, reason string) {
	trial.Valid = false
	if trial.InvalidReason == "" {
		trial.InvalidReason = reason
	} else if !strings.Contains(trial.InvalidReason, reason) {
		trial.InvalidReason += ";" + reason
	}
}

func abs(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}

func percentile(sorted []float64, quantile float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	index := int(float64(len(sorted)-1) * quantile)
	return sorted[index]
}

func validateConfig(config Config) error {
	if config.RunID == "" || strings.ContainsAny(config.RunID, `/\`) {
		return errors.New("run ID must be a non-empty path segment")
	}
	if config.Storage != StorageLocal && config.Storage != StorageS3 {
		return fmt.Errorf("storage must be %s or %s", StorageLocal, StorageS3)
	}
	if config.Storage == StorageS3 && (config.OriginalBucket == "" || config.DerivativeBucket == "") {
		return errors.New("S3 storage requires original and derivative buckets")
	}
	if len(config.Modes) == 0 || len(config.Faults) == 0 {
		return errors.New("at least one mode and one fault are required")
	}
	for _, mode := range config.Modes {
		if _, err := media.ParseIsolationMode(string(mode)); err != nil {
			return err
		}
	}
	for _, fault := range config.Faults {
		if _, err := media.ParseFaultKind(string(fault)); err != nil {
			return err
		}
	}
	if config.Repetitions < 1 || config.TransformConcurrency < 1 || config.HitKeys < 1 || config.MaxInflight < 1 {
		return errors.New("repetitions, transform concurrency, hit keys and max inflight must be positive")
	}
	if config.RequestTimeout <= 0 || config.TransformTimeout <= 0 || config.SlotWaitLimit <= 0 || config.FaultDelay <= 0 {
		return errors.New("request timeout, transform timeout, slot wait limit and fault delay must be positive")
	}
	if config.RequestTimeout <= config.TransformTimeout {
		return errors.New("request timeout must exceed the transform timeout so the server, not the client, ends slow requests")
	}
	if config.FaultDelay >= config.TransformTimeout {
		return errors.New("fault delay must be shorter than the transform timeout so slow faults succeed")
	}
	if config.NormalDuration <= 0 || config.FaultDuration <= 0 || config.RecoveryDuration <= 0 {
		return errors.New("timeline phases must be positive")
	}
	if config.KillSwitchOnDelay < 0 || config.KillSwitchOnDelay >= config.FaultDuration || config.KillSwitchOffDelay < 0 || config.KillSwitchOffDelay >= config.RecoveryDuration {
		return errors.New("kill switch delays must fall inside the fault and recovery phases")
	}
	if config.HitRate <= 0 || config.HealthyMissRate <= 0 || config.PoisonedMissRate <= 0 {
		return errors.New("stream rates must be positive")
	}
	if config.ResourceSampleGap <= 0 || config.StateSampleGap <= 0 || config.TimelineBucket <= 0 {
		return errors.New("sample gaps and timeline bucket must be positive")
	}
	return nil
}
