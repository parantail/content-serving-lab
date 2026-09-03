package main

import (
	"strings"
	"testing"
)

func TestExperimentModeIsOptIn(t *testing.T) {
	t.Setenv("E1_EXPERIMENT_MODE", "")
	controller, err := newExperimentController(nil, nil)
	if err != nil || controller != nil {
		t.Fatalf("unset experiment mode = %v, %v; want nil, nil", controller, err)
	}

	t.Setenv("E1_EXPERIMENT_MODE", "false")
	controller, err = newExperimentController(nil, nil)
	if err != nil || controller != nil {
		t.Fatalf("disabled experiment mode = %v, %v; want nil, nil", controller, err)
	}
}

func TestExperimentModeRejectsInvalidOrMissingMetadataConfiguration(t *testing.T) {
	t.Setenv("E1_EXPERIMENT_MODE", "invalid")
	if _, err := newExperimentController(nil, nil); err == nil || !strings.Contains(err.Error(), "invalid E1_EXPERIMENT_MODE") {
		t.Fatalf("invalid mode error = %v", err)
	}

	t.Setenv("E1_EXPERIMENT_MODE", "true")
	t.Setenv("ECS_CONTAINER_METADATA_URI_V4", "")
	if _, err := newExperimentController(nil, nil); err == nil || !strings.Contains(err.Error(), "ECS_CONTAINER_METADATA_URI_V4") {
		t.Fatalf("missing metadata URI error = %v", err)
	}
}
