package e1awss4runner

import (
	"context"
	"errors"
	"testing"
	"time"
)

func retainedTestConfig(root string) Config {
	c := testConfig(root, 10)
	c.Calibration = false
	c.RunID = "retained-" + c.GitCommit[:12]
	c.StartSkewLimit = 50 * time.Millisecond
	c.RequestTimeout = 90 * time.Second
	c.ControlTimeout = 30 * time.Second
	c.ControlPollGap = 100 * time.Millisecond
	return c
}

func TestRetainedConfigRejectsDrift(t *testing.T) {
	base := retainedTestConfig(t.TempDir())
	if err := ValidateConfig(base); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Config){
		"mode ID":         func(c *Config) { c.RunID = "calibration-test" },
		"repetitions":     func(c *Config) { c.Repetitions = 9 },
		"skew":            func(c *Config) { c.StartSkewLimit = time.Second },
		"request timeout": func(c *Config) { c.RequestTimeout = time.Second },
		"control timeout": func(c *Config) { c.ControlTimeout = time.Second },
		"poll":            func(c *Config) { c.ControlPollGap = time.Second },
		"region":          func(c *Config) { c.Region = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			c := base
			change(&c)
			if ValidateConfig(c) == nil {
				t.Fatal("accepted drift")
			}
		})
	}
}

func TestRetainedRoundTripAndTampering(t *testing.T) {
	c := retainedTestConfig(t.TempDir())
	remote := newFakeRemote(c)
	for scenario, tasks := range remote.tasks {
		for i := range tasks {
			tasks[i].ResourceSource = RetainedResourceSource
			tasks[i].ResourceSampleGapMS = 50
		}
		remote.tasks[scenario] = tasks
	}
	output, analysis, err := Execute(context.Background(), c, Dependencies{Storage: &fakeStorage{derivative: testWebP()}, Probe: remote, HTTPClient: remote})
	if err != nil {
		t.Fatal(err)
	}
	if analysis.ValidTrials != 30 || analysis.InvalidTrials != 0 {
		t.Fatalf("analysis=%+v", analysis)
	}
	check := func() error {
		return validateRaw(output.Metadata, output.Infrastructure, output.Cost, output.Trials, output.Requests, output.Tasks, output.Resources, output.Storage, output.Events)
	}
	metadata := output.Metadata
	for _, mutate := range []func(){
		func() { output.Metadata.MeasurementContract = "" },
		func() { output.Metadata.ResourceSampleGapMS = 100 },
		func() { output.Metadata.StartSkewLimitMS = 100 },
		func() { output.Metadata.Repetitions = 9 },
	} {
		mutate()
		if check() == nil {
			t.Fatal("accepted altered metadata")
		}
		output.Metadata = metadata
	}
	task := output.Tasks[0].Task
	for _, source := range []string{"", "ecs-metadata", "cgroup-v2-container-visible"} {
		output.Tasks[0].Task.ResourceSource = source
		if check() == nil {
			t.Fatal("accepted altered source")
		}
	}
	output.Tasks[0].Task = task
	output.Tasks[0].Task.ResourceSampleGapMS = 100
	if check() == nil {
		t.Fatal("accepted altered sample gap")
	}
	output.Tasks[0].Task = task
	if check() != nil {
		t.Fatal("restored output rejected")
	}
	if _, _, err := Execute(context.Background(), c, Dependencies{Storage: &fakeStorage{}, Probe: remote, HTTPClient: remote}); err == nil {
		t.Fatal("overwrote local results")
	}
}

type reservedStorage struct{ fakeStorage }

func (*reservedStorage) ReserveRun(context.Context, string, string) error {
	return errors.New("already reserved")
}

func TestReservationFailurePreventsWorkload(t *testing.T) {
	c := testConfig(t.TempDir(), 1)
	remote := newFakeRemote(c)
	if _, _, err := Execute(context.Background(), c, Dependencies{Storage: &reservedStorage{}, Probe: remote, HTTPClient: remote}); err == nil {
		t.Fatal("accepted occupied remote run")
	}
	if len(remote.counts) != 0 {
		t.Fatal("workload started despite reservation failure")
	}
}
