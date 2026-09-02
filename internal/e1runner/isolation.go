package e1runner

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

	"github.com/parantail/content-serving-lab/internal/media"
)

type trialInvocation struct {
	Config      Config `json:"config"`
	ScenarioID  string `json:"scenario_id"`
	Mode        string `json:"mode"`
	Concurrency int    `json:"concurrency"`
	Failure     bool   `json:"failure"`
	Repetition  int    `json:"repetition"`
}

type isolatedTrialOutput struct {
	Trial     TrialResult           `json:"trial"`
	Requests  []RequestResult       `json:"requests"`
	Resources []ResourceSample      `json:"resources"`
	Metrics   media.MetricsSnapshot `json:"metrics"`
}

func executeIsolatedTrial(config Config, item scheduledScenario) (TrialResult, []RequestResult, []ResourceSample, media.MetricsSnapshot, error) {
	directory, err := os.MkdirTemp("", "content-serving-e1-invocation-")
	if err != nil {
		return TrialResult{}, nil, nil, media.MetricsSnapshot{}, err
	}
	defer os.RemoveAll(directory)
	inputPath := filepath.Join(directory, "input.json")
	outputPath := filepath.Join(directory, "output.json")
	invocation := trialInvocation{
		Config: config, ScenarioID: item.ID, Mode: item.Mode, Concurrency: item.Concurrency,
		Failure: item.Failure, Repetition: item.repetition,
	}
	data, err := json.Marshal(invocation)
	if err != nil {
		return TrialResult{}, nil, nil, media.MetricsSnapshot{}, err
	}
	if err := os.WriteFile(inputPath, data, 0o600); err != nil {
		return TrialResult{}, nil, nil, media.MetricsSnapshot{}, err
	}
	executable, err := os.Executable()
	if err != nil {
		return TrialResult{}, nil, nil, media.MetricsSnapshot{}, err
	}
	childDeadline := config.RequestTimeout + config.TransformTimeout + 30*time.Second
	ctx, cancel := context.WithTimeout(context.Background(), childDeadline)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "_trial", "--input", inputPath, "--output", outputPath)
	command.Stdout = os.Stderr
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return TrialResult{}, nil, nil, media.MetricsSnapshot{}, fmt.Errorf("isolated trial exceeded %s: %w", childDeadline, ctx.Err())
		}
		return TrialResult{}, nil, nil, media.MetricsSnapshot{}, fmt.Errorf("isolated trial process: %w", err)
	}
	data, err = os.ReadFile(outputPath)
	if err != nil {
		return TrialResult{}, nil, nil, media.MetricsSnapshot{}, fmt.Errorf("read isolated trial output: %w", err)
	}
	var output isolatedTrialOutput
	if err := json.Unmarshal(data, &output); err != nil {
		return TrialResult{}, nil, nil, media.MetricsSnapshot{}, fmt.Errorf("decode isolated trial output: %w", err)
	}
	return output.Trial, output.Requests, output.Resources, output.Metrics, nil
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
	spec, err := media.ParseTransformSpec("width=640,height=640,fit=cover,quality=80", "webp")
	if err != nil {
		return err
	}
	transformer := media.NewVipsTransformerWithConcurrency(invocation.Config.TransformConcurrency)
	if err := warmUp(transformer, fixture); err != nil {
		return fmt.Errorf("warm up transformer: %w", err)
	}
	hostname, _ := os.Hostname()
	item := scheduledScenario{scenario: scenario{
		ID: invocation.ScenarioID, Mode: invocation.Mode, Concurrency: invocation.Concurrency, Failure: invocation.Failure,
	}, repetition: invocation.Repetition}
	trial, requests, resources, snapshot, err := executeTrial(invocation.Config, item, fixture, sourceHash, spec, hostname)
	if err != nil {
		return err
	}
	output := isolatedTrialOutput{Trial: trial, Requests: requests, Resources: resources, Metrics: snapshot}
	data, err = json.Marshal(output)
	if err != nil {
		return err
	}
	return os.WriteFile(outputPath, data, 0o600)
}
