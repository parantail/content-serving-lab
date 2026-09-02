package e1phaseb

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parantail/content-serving-lab/internal/media"
)

func TestAnalyzeCrossValidatesPhaseBRaw(t *testing.T) {
	t.Parallel()

	output := syntheticRunOutput(t.TempDir())
	if err := writeRunOutput(output); err != nil {
		t.Fatal(err)
	}
	analysis, err := Analyze(output.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if analysis.ValidTrials != 7 || analysis.InvalidTrials != 0 {
		t.Fatalf("analysis counts = %d valid, %d invalid", analysis.ValidTrials, analysis.InvalidTrials)
	}
	for _, name := range []string{"analysis.json", "summary.csv", "unrelated-latency.svg", "cancellation-timeline.svg", "multiprocess-work.svg"} {
		if !fileExists(filepath.Join(analysis.Directory, name)) {
			t.Errorf("missing analysis file %s", name)
		}
	}

	output.Requests = output.Requests[:len(output.Requests)-1]
	if err := writeRequests(filepath.Join(output.Directory, "requests.csv"), output.Requests); err != nil {
		t.Fatal(err)
	}
	if _, err := Analyze(output.Directory); err == nil || !strings.Contains(err.Error(), "request totals") {
		t.Fatalf("Analyze() error = %v, want request totals failure", err)
	}
}

func syntheticRunOutput(root string) RunOutput {
	now := time.Now().UTC()
	metadata := NewRunMetadata(Config{RunID: "synthetic", Repetitions: 1})
	metadata.RunID = "synthetic"
	output := RunOutput{Directory: filepath.Join(root, "synthetic"), Metadata: metadata}
	type definition struct {
		id           string
		requests     int
		processCount int
	}
	definitions := []definition{
		{ScenarioS3ColdControl, 10, 0}, {ScenarioS3Cold, 100, 0}, {ScenarioS3WarmControl, 10, 0},
		{ScenarioS3Warm, 100, 0}, {ScenarioF2, 11, 0}, {ScenarioS42, 100, 2}, {ScenarioS44, 100, 4},
	}
	for _, definition := range definitions {
		id := definition.id + "-r01"
		trial := TrialResult{RunID: "synthetic", TrialID: id, Scenario: definition.id, Repetition: 1, ProcessCount: definition.processCount, Valid: true, TotalRequests: definition.requests, SuccessRequests: definition.requests}
		switch definition.id {
		case ScenarioS3ColdControl:
			trial.TransformAttempts, trial.OriginalReads, trial.PublishCreated, trial.DerivativeFiles = 1, 1, 1, 1
		case ScenarioS3Cold:
			trial.TransformAttempts, trial.TransformMaxInflight, trial.OriginalReads, trial.PublishCreated, trial.DerivativeFiles = 2, 2, 2, 2, 2
		case ScenarioS3WarmControl:
			trial.DerivativeHits, trial.DerivativeFiles = 10, 1
		case ScenarioS3Warm:
			trial.DerivativeHits, trial.TransformAttempts, trial.OriginalReads, trial.PublishCreated, trial.DerivativeFiles = 10, 1, 1, 1, 2
		case ScenarioF2:
			trial.SuccessRequests = 10
			trial.CanceledRequests = 1
			trial.DerivativeHits, trial.TransformAttempts, trial.TransformMaxInflight, trial.OriginalReads = 1, 1, 1, 1
			trial.PublishCreated, trial.CoalescedRequests, trial.DerivativeFiles = 1, 9, 1
		case ScenarioS42, ScenarioS44:
			trial.ActiveTasks = definition.processCount
			trial.TransformAttempts = int64(definition.processCount)
			trial.TransformMaxInflight = 1
			trial.OriginalReads = int64(definition.processCount)
			trial.PublishCreated = 1
			trial.PublishExisting = int64(definition.processCount - 1)
			trial.DerivativeFiles = 1
		}
		output.Trials = append(output.Trials, trial)
		taskCount := definition.processCount
		if taskCount == 0 {
			taskCount = 1
		}
		for task := 0; task < taskCount; task++ {
			snapshot := media.MetricsSnapshot{}
			if task == 0 {
				snapshot.DerivativeHits = trial.DerivativeHits
				snapshot.TransformSuccess = trial.TransformAttempts
				snapshot.TransformMaxInflight = trial.TransformMaxInflight
				snapshot.OriginalSuccess = trial.OriginalReads
				snapshot.PublishCreated = trial.PublishCreated
				snapshot.PublishExisting = trial.PublishExisting
				snapshot.Coalesced = trial.CoalescedRequests
				if definition.processCount > 0 {
					snapshot.TransformSuccess = 1
					snapshot.OriginalSuccess = 1
					snapshot.PublishExisting = 0
				}
			} else if definition.processCount > 0 {
				snapshot.TransformSuccess = 1
				snapshot.TransformMaxInflight = 1
				snapshot.OriginalSuccess = 1
				snapshot.PublishExisting = 1
			}
			output.Metrics = append(output.Metrics, MetricRecord{TrialID: id, Scenario: definition.id, TaskID: fmt.Sprintf("process-%02d", task+1), Snapshot: snapshot})
		}
		for index := 0; index < definition.requests; index++ {
			class, role, errorType, status := RequestClassUnrelated, RequestRoleBurst, "", 200
			if definition.id == ScenarioS3Cold || definition.id == ScenarioS3Warm {
				if index%10 != 9 {
					class = RequestClassHot
				}
			}
			if definition.id == ScenarioF2 {
				class = RequestClassHot
				role = RequestRoleWaiter
				if index == 0 {
					role, errorType, status = RequestRoleLeader, "canceled", 0
				} else if index == definition.requests-1 {
					class, role = RequestClassFollowUp, RequestRoleFollowUp
				}
			}
			if definition.id == ScenarioS42 || definition.id == ScenarioS44 {
				class = RequestClassHot
			}
			taskID := fmt.Sprintf("process-%02d", index%taskCount+1)
			output.Requests = append(output.Requests, RequestResult{
				RunID: "synthetic", TrialID: id, Scenario: definition.id, RequestID: fmt.Sprintf("request-%03d", index+1),
				RequestClass: class, RequestRole: role, TargetTaskID: taskID, TaskID: taskID,
				StartedAt: now.Format(time.RFC3339Nano), FinishedAt: now.Add(time.Millisecond).Format(time.RFC3339Nano),
				LatencyMS: 1, HTTPStatus: status, ResponseSHA256: "same-hash", ErrorType: errorType,
			})
		}
		if definition.id == ScenarioF2 {
			for index, name := range []string{"leader_transform_started", "waiters_joined", "leader_cancel_sent", "leader_finished", "waiters_finished", "follow_up_finished"} {
				output.Events = append(output.Events, Event{RunID: "synthetic", TrialID: id, Scenario: ScenarioF2, Name: name, Timestamp: now.Add(time.Duration(index) * time.Millisecond).Format(time.RFC3339Nano)})
			}
		}
	}
	return output
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
