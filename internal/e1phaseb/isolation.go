package e1phaseb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type trialInvocation struct {
	Config   Config   `json:"config"`
	Scenario scenario `json:"scenario"`
}

type isolatedTrialOutput struct {
	Trial     TrialResult      `json:"trial"`
	Requests  []RequestResult  `json:"requests"`
	Resources []ResourceSample `json:"resources"`
	Metrics   []MetricRecord   `json:"metrics"`
	Events    []Event          `json:"events"`
}

func executeIsolatedTrial(config Config, item scenario) (TrialResult, []RequestResult, []ResourceSample, []MetricRecord, []Event, error) {
	directory, err := os.MkdirTemp("", "content-serving-e1-phase-b-invocation-")
	if err != nil {
		return TrialResult{}, nil, nil, nil, nil, err
	}
	defer os.RemoveAll(directory)
	inputPath := filepath.Join(directory, "input.json")
	outputPath := filepath.Join(directory, "output.json")
	data, err := json.Marshal(trialInvocation{Config: config, Scenario: item})
	if err != nil {
		return TrialResult{}, nil, nil, nil, nil, err
	}
	if err := os.WriteFile(inputPath, data, 0o600); err != nil {
		return TrialResult{}, nil, nil, nil, nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return TrialResult{}, nil, nil, nil, nil, err
	}
	childDeadline := config.RequestTimeout + config.TransformTimeout + 60*time.Second
	ctx, cancel := context.WithTimeout(context.Background(), childDeadline)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "_trial", "--input", inputPath, "--output", outputPath)
	command.Stdout = os.Stderr
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return TrialResult{}, nil, nil, nil, nil, fmt.Errorf("isolated trial exceeded %s: %w", childDeadline, ctx.Err())
		}
		return TrialResult{}, nil, nil, nil, nil, fmt.Errorf("isolated trial process: %w", err)
	}
	data, err = os.ReadFile(outputPath)
	if err != nil {
		return TrialResult{}, nil, nil, nil, nil, fmt.Errorf("read isolated trial output: %w", err)
	}
	var output isolatedTrialOutput
	if err := json.Unmarshal(data, &output); err != nil {
		return TrialResult{}, nil, nil, nil, nil, fmt.Errorf("decode isolated trial output: %w", err)
	}
	return output.Trial, output.Requests, output.Resources, output.Metrics, output.Events, nil
}

func ExecuteTrialFile(inputPath, outputPath string) error {
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return fmt.Errorf("read trial invocation: %w", err)
	}
	var invocation trialInvocation
	if err := json.Unmarshal(data, &invocation); err != nil {
		return fmt.Errorf("decode trial invocation: %w", err)
	}
	fixture, err := os.ReadFile(invocation.Config.FixturePath)
	if err != nil {
		return fmt.Errorf("read fixture: %w", err)
	}
	digest := sha256.Sum256(fixture)
	sourceHash := hex.EncodeToString(digest[:])
	hostname, _ := os.Hostname()
	trial, requests, resources, metrics, events, err := executeTrial(invocation.Config, invocation.Scenario, fixture, sourceHash, hostname)
	if err != nil {
		return err
	}
	data, err = json.Marshal(isolatedTrialOutput{Trial: trial, Requests: requests, Resources: resources, Metrics: metrics, Events: events})
	if err != nil {
		return err
	}
	return os.WriteFile(outputPath, data, 0o600)
}
