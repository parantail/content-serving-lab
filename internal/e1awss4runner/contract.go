package e1awss4runner

import (
	"errors"
	"time"

	"github.com/parantail/content-serving-lab/internal/e1awss4"
)

const MeasurementContract = "e1-aws-s4-retained-v1"
const RetainedResourceSource = "cgroup-v1-container-visible"

func validateRetainedConfig(c Config) error {
	if c.Calibration {
		return nil
	}
	if len(c.GitCommit) != 40 || c.RunID != "retained-"+c.GitCommit[:12] || c.Region != "ap-northeast-2" ||
		c.Repetitions != 10 || c.StartSkewLimit != 50*time.Millisecond ||
		c.RequestTimeout != 90*time.Second || c.ControlTimeout != 30*time.Second || c.ControlPollGap != 100*time.Millisecond ||
		c.SourceHash != "de206136ed0eeaa42499840f49fa35bd3d57745b8a9580b16f0d6e6393ac7f91" ||
		c.CanonicalSpec != "width=640,height=640,fit=cover,quality=80,format=webp" {
		return errors.New("retained measurement contract mismatch: run ID, fixture, region, repetitions or timing")
	}
	return nil
}

func validateRetainedTask(task e1awss4.TaskIdentity) error {
	if task.ResourceSource != RetainedResourceSource || task.ResourceSampleGapMS != 50 || task.CPUVCpu != 1 || task.MemoryMiB != 2048 || task.LaunchType != "FARGATE" {
		return errors.New("retained task source, sampling interval or resource size mismatch")
	}
	return nil
}
