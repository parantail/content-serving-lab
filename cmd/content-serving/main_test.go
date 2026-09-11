package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	if _, _, err := newStores(nil, ""); err == nil || !strings.Contains(err.Error(), "invalid STORAGE_BACKEND") {
		t.Fatalf("invalid backend error = %v", err)
	}

	t.Setenv("STORAGE_BACKEND", "s3")
	t.Setenv("ORIGINAL_BUCKET", "")
	t.Setenv("DERIVATIVE_BUCKET", "")
	if _, _, err := newStores(nil, ""); err == nil || !strings.Contains(err.Error(), "ORIGINAL_BUCKET") {
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

	originals, derivatives, err := newStores(&media.S3Metrics{}, "")
	if err != nil {
		t.Fatalf("newStores() error = %v", err)
	}
	if originals == nil || derivatives == nil {
		t.Fatal("newStores() returned a nil S3 store")
	}
}

func TestIsolationModeDefaultsToBaselineAndValidatesInput(t *testing.T) {
	t.Setenv("ISOLATION_MODE", "")
	t.Setenv("E3_SLOT_WAIT_LIMIT", "")
	isolation, err := newIsolation(4, &media.Metrics{})
	if err != nil {
		t.Fatalf("default isolation error = %v", err)
	}
	if isolation.Mode != media.IsolationBaseline || isolation.KillSwitch != nil || isolation.Gate.WaitLimit() != 0 || isolation.Gate.Concurrency() != 4 {
		t.Fatalf("default isolation = %+v", isolation)
	}

	t.Setenv("ISOLATION_MODE", "bounded-wait")
	t.Setenv("E3_SLOT_WAIT_LIMIT", "750ms")
	isolation, err = newIsolation(2, &media.Metrics{})
	if err != nil {
		t.Fatalf("bounded-wait isolation error = %v", err)
	}
	if isolation.Mode != media.IsolationBoundedWait || isolation.Gate.WaitLimit().String() != "750ms" {
		t.Fatalf("bounded-wait isolation = %+v", isolation)
	}

	t.Setenv("ISOLATION_MODE", "kill-switch")
	isolation, err = newIsolation(2, &media.Metrics{})
	if err != nil || isolation.KillSwitch == nil {
		t.Fatalf("kill-switch isolation = %+v, err = %v", isolation, err)
	}

	t.Setenv("ISOLATION_MODE", "multi-region")
	if _, err := newIsolation(2, &media.Metrics{}); err == nil || !strings.Contains(err.Error(), "invalid ISOLATION_MODE") {
		t.Fatalf("invalid mode error = %v", err)
	}
	t.Setenv("ISOLATION_MODE", "bounded-wait")
	t.Setenv("E3_SLOT_WAIT_LIMIT", "0s")
	if _, err := newIsolation(2, &media.Metrics{}); err == nil || !strings.Contains(err.Error(), "invalid E3_SLOT_WAIT_LIMIT") {
		t.Fatalf("invalid wait limit error = %v", err)
	}
}

func TestIsolationControlIsOptIn(t *testing.T) {
	t.Setenv("E3_CONTROL_MODE", "")
	if controller, err := newIsolationController(nil); err != nil || controller != nil {
		t.Fatalf("unset control mode = %v, %v; want nil, nil", controller, err)
	}
	t.Setenv("E3_CONTROL_MODE", "false")
	if controller, err := newIsolationController(nil); err != nil || controller != nil {
		t.Fatalf("disabled control mode = %v, %v; want nil, nil", controller, err)
	}
	t.Setenv("E3_CONTROL_MODE", "maybe")
	if _, err := newIsolationController(nil); err == nil || !strings.Contains(err.Error(), "invalid E3_CONTROL_MODE") {
		t.Fatalf("invalid control mode error = %v", err)
	}
	t.Setenv("E3_CONTROL_MODE", "true")
	if _, err := newIsolationController(nil); err == nil {
		t.Fatal("control mode with nil processor accepted")
	}
}

func TestPoisonedSourceHashAliasesLocalOriginal(t *testing.T) {
	directory := t.TempDir()
	sourceFile := filepath.Join(directory, "original.jpg")
	if err := os.WriteFile(sourceFile, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STORAGE_BACKEND", "local")
	t.Setenv("SOURCE_FILE", sourceFile)
	t.Setenv("DERIVATIVE_DIR", filepath.Join(directory, "derivatives"))

	const poisoned = "ABABABABABABABABABABABABABABABABABABABABABABABABABABABABABABABAB"
	originals, _, err := newStores(nil, poisoned)
	if err != nil {
		t.Fatalf("newStores() error = %v", err)
	}
	data, err := originals.Read(context.Background(), strings.ToLower(poisoned))
	if err != nil || string(data) != "original" {
		t.Fatalf("poisoned alias read = %q, %v", data, err)
	}
	if _, err := originals.Read(context.Background(), strings.Repeat("00", 32)); !errors.Is(err, media.ErrOriginalNotFound) {
		t.Fatalf("unknown hash error = %v, want ErrOriginalNotFound", err)
	}
}
