package e1phaseb

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parantail/content-serving-lab/internal/httpapi"
	"github.com/parantail/content-serving-lab/internal/media"
)

type ServerConfig struct {
	ReadyFile            string
	FixturePath          string
	DerivativeDirectory  string
	TaskID               string
	TransformTimeout     time.Duration
	TransformConcurrency int
}

type serverReady struct {
	Address string `json:"address"`
	TaskID  string `json:"task_id"`
}

type runningServer struct {
	taskID  string
	address string
	command *exec.Cmd
	cancel  context.CancelFunc
}

func Serve(config ServerConfig) error {
	fixture, err := os.ReadFile(config.FixturePath)
	if err != nil {
		return fmt.Errorf("read fixture: %w", err)
	}
	digest := sha256.Sum256(fixture)
	sourceHash := hex.EncodeToString(digest[:])
	derivatives, err := media.NewLocalDerivativeStore(config.DerivativeDirectory)
	if err != nil {
		return err
	}
	transformer := media.NewVipsTransformerWithConcurrency(config.TransformConcurrency)
	if err := warmUp(transformer, fixture); err != nil {
		return fmt.Errorf("warm up transformer: %w", err)
	}
	metrics := &media.Metrics{}
	processor := media.NewProcessor(
		media.NewFileOriginalStore(map[string]string{sourceHash: config.FixturePath}),
		derivatives,
		transformer,
		media.NewProcessCoordinator(config.TransformTimeout),
		metrics,
	)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	readyData, err := json.Marshal(serverReady{Address: "http://" + listener.Addr().String(), TaskID: config.TaskID})
	if err != nil {
		return err
	}
	if err := writeAtomic(config.ReadyFile, append(readyData, '\n')); err != nil {
		return err
	}
	server := &http.Server{Handler: withTaskID(httpapi.NewHandler(processor), config.TaskID), ReadHeaderTimeout: 5 * time.Second}
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func executeMultiProcessTrial(config Config, item scenario, sourceHash, hostname string) (TrialResult, []RequestResult, []ResourceSample, []MetricRecord, []Event, error) {
	trialID := trialID(item)
	directory, err := os.MkdirTemp("", "content-serving-e1-phase-b-"+trialID+"-")
	if err != nil {
		return TrialResult{}, nil, nil, nil, nil, err
	}
	defer os.RemoveAll(directory)
	derivativeDirectory := filepath.Join(directory, "derivatives")
	if err := os.MkdirAll(derivativeDirectory, 0o755); err != nil {
		return TrialResult{}, nil, nil, nil, nil, err
	}

	servers, events, err := startServers(config, item, derivativeDirectory, directory)
	if err != nil {
		return TrialResult{}, nil, nil, nil, nil, err
	}
	defer stopServers(servers)

	sampler := startResourceSampler(config.RunID, trialID, config.ResourceSampleGap)
	requests := issueMultiProcessBurst(config, item, servers, sourceHash, trialID, hostname)
	resources := sampler.finish()
	metricRecords := make([]MetricRecord, 0, len(servers))
	for _, server := range servers {
		snapshot, err := fetchMetrics(server.address + "/metrics")
		if err != nil {
			return TrialResult{}, nil, nil, nil, nil, fmt.Errorf("fetch metrics for %s: %w", server.taskID, err)
		}
		metricRecords = append(metricRecords, MetricRecord{TrialID: trialID, Scenario: item.ID, TaskID: server.taskID, Snapshot: snapshot})
	}
	trial := summarizeTrial(config, item, trialID, requests, resources, metricRecords, countDerivativeFiles(derivativeDirectory))
	return trial, requests, resources, metricRecords, events, nil
}

func startServers(config Config, item scenario, derivativeDirectory, directory string) ([]runningServer, []Event, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	servers := make([]runningServer, 0, item.ProcessCount)
	events := make([]Event, 0, item.ProcessCount)
	for index := 0; index < item.ProcessCount; index++ {
		taskID := fmt.Sprintf("process-%02d", index+1)
		readyFile := filepath.Join(directory, taskID+"-ready.json")
		ctx, cancel := context.WithCancel(context.Background())
		command := exec.CommandContext(ctx, executable,
			"_serve",
			"--ready-file", readyFile,
			"--fixture", config.FixturePath,
			"--derivative-dir", derivativeDirectory,
			"--task-id", taskID,
			"--transform-timeout", config.TransformTimeout.String(),
			"--transform-concurrency", strconv.Itoa(config.TransformConcurrency),
		)
		command.Stdout = os.Stderr
		command.Stderr = os.Stderr
		if err := command.Start(); err != nil {
			cancel()
			stopServers(servers)
			return nil, nil, err
		}
		server := runningServer{taskID: taskID, command: command, cancel: cancel}
		servers = append(servers, server)
		var ready serverReady
		if err := waitForReadyFile(readyFile, command, &ready); err != nil {
			stopServers(servers)
			return nil, nil, fmt.Errorf("start %s: %w", taskID, err)
		}
		servers[len(servers)-1].address = ready.Address
		events = append(events, Event{
			RunID: config.RunID, TrialID: trialID(item), Scenario: item.ID,
			Name: "task_ready", Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Detail: taskID,
		})
	}
	return servers, events, nil
}

func waitForReadyFile(path string, command *exec.Cmd, ready *serverReady) error {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			if err := json.Unmarshal(data, ready); err != nil {
				return err
			}
			if ready.Address == "" || ready.TaskID == "" {
				return errors.New("ready file is incomplete")
			}
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if command.ProcessState != nil && command.ProcessState.Exited() {
			return errors.New("server exited before readiness")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errors.New("server readiness timeout")
}

func stopServers(servers []runningServer) {
	for _, server := range servers {
		server.cancel()
	}
	for _, server := range servers {
		if server.command.Process != nil {
			_ = server.command.Wait()
		}
	}
}

func issueMultiProcessBurst(config Config, item scenario, servers []runningServer, sourceHash, trialID, hostname string) []RequestResult {
	client := burstClient(item.Concurrency)
	defer client.CloseIdleConnections()
	requests := make([]RequestResult, item.Concurrency)
	start := make(chan struct{})
	var ready, finished sync.WaitGroup
	ready.Add(item.Concurrency)
	finished.Add(item.Concurrency)
	for index := 0; index < item.Concurrency; index++ {
		index := index
		go func() {
			defer finished.Done()
			target := servers[index%len(servers)]
			ready.Done()
			<-start
			requestURL := derivativeURL(target.address, sourceHash, hotRawSpec)
			requests[index] = issueRequest(config, item, client, requestURL, trialID, fmt.Sprintf("request-%03d", index+1), RequestClassHot, RequestRoleBurst, target.taskID, hostname)
		}()
	}
	ready.Wait()
	close(start)
	finished.Wait()
	return requests
}

func fetchMetrics(metricsURL string) (media.MetricsSnapshot, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(metricsURL)
	if err != nil {
		return media.MetricsSnapshot{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return media.MetricsSnapshot{}, fmt.Errorf("metrics status %d", response.StatusCode)
	}
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return media.MetricsSnapshot{}, err
	}
	values := parsePrometheus(data)
	return media.MetricsSnapshot{
		DerivativeHits:       values[`media_derivative_requests_total{result="hit"}`],
		DerivativeMisses:     values[`media_derivative_requests_total{result="miss"}`],
		OriginalSuccess:      values[`media_original_reads_total{result="success"}`],
		OriginalError:        values[`media_original_reads_total{result="error"}`],
		TransformSuccess:     values[`media_transform_attempts_total{result="success"}`],
		TransformError:       values[`media_transform_attempts_total{result="error"}`],
		TransformTimeout:     values[`media_transform_attempts_total{result="timeout"}`],
		TransformInflight:    values["media_transform_inflight"],
		TransformMaxInflight: values["media_transform_max_inflight"],
		Coalesced:            values["media_requests_coalesced_total"],
		PublishCreated:       values[`media_derivative_publish_attempts_total{result="created"}`],
		PublishExisting:      values[`media_derivative_publish_attempts_total{result="existing"}`],
		PublishError:         values[`media_derivative_publish_attempts_total{result="error"}`],
	}, nil
}

func parsePrometheus(data []byte) map[string]int64 {
	values := make(map[string]int64)
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		value, err := strconv.ParseInt(fields[1], 10, 64)
		if err == nil {
			values[fields[0]] = value
		}
	}
	return values
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".ready-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
