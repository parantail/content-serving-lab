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
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	processor, err := newProcessor()
	if err != nil {
		return err
	}

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           httpapi.NewHandler(processor),
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
	)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func newProcessor() (*media.Processor, error) {
	sourceFile := os.Getenv("SOURCE_FILE")
	if sourceFile == "" {
		sourceFile = filepath.FromSlash("experiments/e1-cache-stampede/fixtures/landscape-4928x3264.jpg")
	}
	original, err := os.ReadFile(sourceFile)
	if err != nil {
		return nil, fmt.Errorf("read SOURCE_FILE: %w", err)
	}
	sourceDigest := sha256.Sum256(original)
	sourceHash := fmt.Sprintf("%x", sourceDigest)

	derivativeDirectory := os.Getenv("DERIVATIVE_DIR")
	if derivativeDirectory == "" {
		derivativeDirectory = filepath.Join(os.TempDir(), "content-serving", "derivatives")
	}
	derivatives, err := media.NewLocalDerivativeStore(derivativeDirectory)
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
		media.NewFileOriginalStore(map[string]string{sourceHash: sourceFile}),
		derivatives,
		media.NewVipsTransformerWithConcurrency(transformConcurrency),
		coordinator,
		&media.Metrics{},
	), nil
}
