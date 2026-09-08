package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/parantail/content-serving-lab/internal/e1awss4"
	"github.com/parantail/content-serving-lab/internal/httpapi"
	"github.com/parantail/content-serving-lab/internal/media"
)

var gitCommit = "unknown"

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	if os.Getenv("E1_RESOURCE_DIAGNOSTIC") == "true" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		ctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		return e1awss4.RunResourceDiagnostic(ctx, os.Stdout, gitCommit)
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	storageMetrics := &media.S3Metrics{}
	processor, err := newProcessor(storageMetrics)
	if err != nil {
		return err
	}
	experiment, err := newExperimentController(processor, storageMetrics)
	if err != nil {
		return err
	}

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           httpapi.NewHandlerWithExperiment(processor, experiment),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Error("graceful shutdown failed", "error", err)
		}
	}()

	slog.Info(
		"server listening",
		"address", server.Addr,
		"git_commit", gitCommit,
		"coordinator", processor.CoordinatorMode(),
		"experiment_mode", experiment != nil,
	)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func newExperimentController(processor *media.Processor, storageMetrics *media.S3Metrics) (*e1awss4.Controller, error) {
	rawMode := os.Getenv("E1_EXPERIMENT_MODE")
	if rawMode == "" {
		return nil, nil
	}
	enabled, err := strconv.ParseBool(rawMode)
	if err != nil {
		return nil, fmt.Errorf("invalid E1_EXPERIMENT_MODE %q", rawMode)
	}
	if !enabled {
		return nil, nil
	}

	metadataURI := os.Getenv("ECS_CONTAINER_METADATA_URI_V4")
	if metadataURI == "" {
		return nil, errors.New("ECS_CONTAINER_METADATA_URI_V4 is required in experiment mode")
	}
	containerName := os.Getenv("E1_CONTAINER_NAME")
	if containerName == "" {
		containerName = "media-service"
	}
	metadataClient, err := e1awss4.NewMetadataClient(metadataURI, containerName, &http.Client{Timeout: 2 * time.Second})
	if err != nil {
		return nil, err
	}
	metadataCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	identity, err := metadataClient.LoadTaskIdentity(metadataCtx)
	if err != nil {
		return nil, err
	}
	slog.Info(
		"experiment task metadata loaded",
		"task_id", identity.TaskID,
		"task_definition_family", identity.TaskDefinitionFamily,
		"task_definition_revision", identity.TaskDefinitionRevision,
		"availability_zone", identity.AvailabilityZone,
		"launch_type", identity.LaunchType,
		"cpu_vcpu", identity.CPUVCpu,
		"memory_mib", identity.MemoryMiB,
		"image_digest", identity.ImageDigest,
	)
	resourceSource, err := e1awss4.NewCgroupSource("/sys/fs/cgroup")
	if err != nil {
		return nil, fmt.Errorf("initialize direct resource counters: %w", err)
	}
	identity.ResourceSource = resourceSource.Name()
	return e1awss4.NewController(identity, processor, storageMetrics, resourceSource, 50*time.Millisecond)
}

func newProcessor(storageMetrics *media.S3Metrics) (*media.Processor, error) {
	originals, derivatives, err := newStores(storageMetrics)
	if err != nil {
		return nil, err
	}
	mode := os.Getenv("COORDINATOR_MODE")
	if mode == "" {
		mode = media.CoordinatorNone
	}
	transformTimeout := 30 * time.Second
	if raw := os.Getenv("TRANSFORM_TIMEOUT"); raw != "" {
		transformTimeout, err = time.ParseDuration(raw)
		if err != nil || transformTimeout <= 0 {
			return nil, fmt.Errorf("invalid TRANSFORM_TIMEOUT %q", raw)
		}
	}
	coordinator, err := media.NewCoordinator(mode, transformTimeout)
	if err != nil {
		return nil, err
	}
	transformConcurrency := media.DefaultTransformConcurrency
	if raw := os.Getenv("TRANSFORM_CONCURRENCY"); raw != "" {
		transformConcurrency, err = strconv.Atoi(raw)
		if err != nil || transformConcurrency < 1 {
			return nil, fmt.Errorf("invalid TRANSFORM_CONCURRENCY %q", raw)
		}
	}

	return media.NewProcessor(
		originals,
		derivatives,
		media.NewVipsTransformerWithConcurrency(transformConcurrency),
		coordinator,
		&media.Metrics{},
	), nil
}

func newStores(storageMetrics *media.S3Metrics) (media.OriginalStore, media.DerivativeStore, error) {
	backend := os.Getenv("STORAGE_BACKEND")
	if backend == "" {
		backend = "local"
	}
	switch backend {
	case "local":
		sourceFile := os.Getenv("SOURCE_FILE")
		if sourceFile == "" {
			sourceFile = filepath.FromSlash("experiments/e1-cache-stampede/fixtures/landscape-4928x3264.jpg")
		}
		original, err := os.ReadFile(sourceFile)
		if err != nil {
			return nil, nil, fmt.Errorf("read SOURCE_FILE: %w", err)
		}
		sourceDigest := sha256.Sum256(original)
		sourceHash := fmt.Sprintf("%x", sourceDigest)
		derivativeDirectory := os.Getenv("DERIVATIVE_DIR")
		if derivativeDirectory == "" {
			derivativeDirectory = filepath.Join(os.TempDir(), "content-serving", "derivatives")
		}
		derivatives, err := media.NewLocalDerivativeStore(derivativeDirectory)
		if err != nil {
			return nil, nil, err
		}
		return media.NewFileOriginalStore(map[string]string{sourceHash: sourceFile}), derivatives, nil
	case "s3":
		originalBucket := os.Getenv("ORIGINAL_BUCKET")
		derivativeBucket := os.Getenv("DERIVATIVE_BUCKET")
		if originalBucket == "" || derivativeBucket == "" {
			return nil, nil, errors.New("ORIGINAL_BUCKET and DERIVATIVE_BUCKET are required for STORAGE_BACKEND=s3")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		configuration, err := awsconfig.LoadDefaultConfig(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("load AWS configuration: %w", err)
		}
		client := s3.NewFromConfig(configuration)
		originals, err := media.NewS3OriginalStore(client, originalBucket, storageMetrics)
		if err != nil {
			return nil, nil, err
		}
		derivatives, err := media.NewS3DerivativeStore(client, derivativeBucket, storageMetrics)
		if err != nil {
			return nil, nil, err
		}
		return originals, derivatives, nil
	default:
		return nil, nil, fmt.Errorf("invalid STORAGE_BACKEND %q", backend)
	}
}
