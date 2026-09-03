package main

import (
	"strings"
	"testing"

	"github.com/parantail/content-serving-lab/internal/media"
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

func TestStorageBackendRejectsInvalidOrIncompleteS3Configuration(t *testing.T) {
	t.Setenv("STORAGE_BACKEND", "invalid")
	if _, _, err := newStores(nil); err == nil || !strings.Contains(err.Error(), "invalid STORAGE_BACKEND") {
		t.Fatalf("invalid backend error = %v", err)
	}

	t.Setenv("STORAGE_BACKEND", "s3")
	t.Setenv("ORIGINAL_BUCKET", "")
	t.Setenv("DERIVATIVE_BUCKET", "")
	if _, _, err := newStores(nil); err == nil || !strings.Contains(err.Error(), "ORIGINAL_BUCKET") {
		t.Fatalf("incomplete S3 backend error = %v", err)
	}
}

func TestStorageBackendBuildsS3StoresFromDefaultAWSConfiguration(t *testing.T) {
	t.Setenv("STORAGE_BACKEND", "s3")
	t.Setenv("ORIGINAL_BUCKET", "test-original")
	t.Setenv("DERIVATIVE_BUCKET", "test-derivative")
	t.Setenv("AWS_REGION", "ap-northeast-2")
	t.Setenv("AWS_ACCESS_KEY_ID", "test-only")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test-only")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	originals, derivatives, err := newStores(&media.S3Metrics{})
	if err != nil {
		t.Fatalf("newStores() error = %v", err)
	}
	if originals == nil || derivatives == nil {
		t.Fatal("newStores() returned a nil S3 store")
	}
}
