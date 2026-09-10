//go:build linux

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/parantail/content-serving-lab/internal/e2"
)

var commit = "development"

const measurementLimit = 210 * time.Minute

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	mode := flag.String("mode", "validate", "validate, quality, calibrate, measure, diagnose")
	output := flag.String("output", "/results/run", "fresh output directory")
	root := flag.String("corpus", "/app/corpus", "corpus directory")
	binaries := flag.String("binaries", "/app", "worker binary directory")
	cohort := flag.String("cohort", "local", "local or aws")
	flag.Parse()
	if *cohort != "local" && *cohort != "aws" {
		return fmt.Errorf("invalid cohort")
	}
	if _, err := os.Stat(*output); !os.IsNotExist(err) {
		return fmt.Errorf("output must not exist")
	}
	c, hash, err := e2.LoadCorpus(*root)
	if err != nil {
		return err
	}
	jobs, err := e2.Matrix(*mode, *root, *output)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(*output, 0755); err != nil {
		return err
	}
	planned, warm := 0, 0
	for _, j := range jobs {
		n := len(c.Fixtures)
		if j.Batch.Quality {
			n = 8
		}
		planned += n
		if j.Batch.Warmup {
			warm += n
		}
	}
	packages, err := exec.Command("dpkg-query", "-W", "-f=${Package}=${Version}\n").Output()
	if err != nil {
		return err
	}
	loopLimit := time.Hour
	if *mode == "measure" {
		loopLimit = measurementLimit
	}
	meta := map[string]any{"schema": "e2-run-v1", "contract": e2.Contract, "commit": commit, "cohort": *cohort, "mode": *mode, "started": time.Now().UTC().Format(time.RFC3339Nano), "corpus_sha256": hash, "go_version": runtime.Version(), "arch": runtime.GOARCH, "packages": string(packages), "jobs": jobs, "planned": planned, "warmup_planned": warm, "max_seconds": int(loopLimit.Seconds()), "measurement_gate_seconds": int(measurementLimit.Seconds()), "transform_timeout_seconds": 30, "sample_interval_ms": 50, "avif_threads_policy": "standard Debian delegate defaults; external 1 vCPU/2 GiB/concurrency matched"}
	if *mode != "diagnose" {
		meta["preflight_diagnostic_calls"] = 16
	}
	meta["codec_quality_contract"] = "effective-avif-q80-v1"
	if err = e2.WriteJSON(filepath.Join(*output, "manifest.json"), meta); err != nil {
		return err
	}
	transfer, err := e2.NewUploader(context.Background(), os.Getenv("E2_RESULTS_BUCKET"), filepath.Base(*output), *output)
	if err != nil {
		return err
	}
	if err = transfer.Flush(); err != nil {
		return err
	}
	parent, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	// Each Fargate task can land on a different host. Probe on this task before
	// the measured loop; no LD_PRELOAD is present in performance workers.
	if *mode != "diagnose" {
		if err = preflight(parent, *root, *output, *binaries); err != nil {
			_ = e2.WriteJSON(filepath.Join(*output, "completion.json"), map[string]any{"schema": "e2-completion-v1", "valid": false, "completed_batches": 0, "planned_batches": len(jobs), "error": err.Error()})
			_ = transfer.Flush()
			return err
		}
		if err = transfer.Flush(); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(parent, loopLimit)
	defer cancel()
	start := time.Now()
	completed := 0
	for i, job := range jobs {
		fmt.Printf("batch %d/%d %s\n", i+1, len(jobs), job.Batch.ID)
		_, err = e2.Supervise(ctx, job, filepath.Join(*binaries, "e2-"+job.Engine), *output, *mode == "diagnose", 30*time.Second)
		if err == nil && *mode == "diagnose" {
			var log []byte
			log, err = os.ReadFile(filepath.Join(*output, job.Batch.ID+".stderr.log"))
			if err == nil {
				_, err = parseCodec(string(log), job.Engine)
			}
		}
		if uploadErr := transfer.Flush(); uploadErr != nil && err == nil {
			err = uploadErr
		}
		if err != nil {
			break
		}
		completed++
	}
	status := map[string]any{"schema": "e2-completion-v1", "completed_batches": completed, "planned_batches": len(jobs), "wall_ns": time.Since(start).Nanoseconds(), "ended": time.Now().UTC().Format(time.RFC3339Nano), "valid": err == nil}
	if err != nil {
		status["error"] = err.Error()
	}
	if writeErr := e2.WriteJSON(filepath.Join(*output, "completion.json"), status); err == nil {
		err = writeErr
	}
	if uploadErr := transfer.Flush(); err == nil {
		err = uploadErr
	}
	return err
}

func preflight(parent context.Context, root, output, binaries string) error {
	path := filepath.Join(output, "preflight")
	if err := os.Mkdir(path, 0755); err != nil {
		return err
	}
	jobs, err := e2.Matrix("diagnose", root, path)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	var settings []map[string]any
	for _, job := range jobs {
		if _, err = e2.Supervise(ctx, job, filepath.Join(binaries, "e2-"+job.Engine), path, true, 30*time.Second); err != nil {
			return err
		}
		log, err := os.ReadFile(filepath.Join(path, job.Batch.ID+".stderr.log"))
		if err != nil {
			return err
		}
		threads, err := parseCodec(string(log), job.Engine)
		if err != nil {
			return err
		}
		settings = append(settings, map[string]any{"engine": job.Engine, "encoder_threads": threads, "speed": 5, "effective_quality": 80, "quality_query_error": 0, "samples": 8})
	}
	return e2.WriteJSON(filepath.Join(path, "settings.json"), map[string]any{"scope": "same task before measured loop", "probe_enabled_in_performance": false, "settings": settings})
}

func parseCodec(log, engine string) (int, error) {
	pattern := regexp.MustCompile(`(?m)^E2_CODEC encoder=AOMedia Project AV1 Encoder v3\.12\.1 threads=(\d+) speed=(\d+) quality=(\d+) query_errors=(\d+),(\d+),(\d+)\r?$`)
	matches := pattern.FindAllStringSubmatch(log, -1)
	if len(matches) != 8 {
		return 0, fmt.Errorf("encoder probe count for %s: %d", engine, len(matches))
	}
	threads := 0
	for _, match := range matches {
		n, _ := strconv.Atoi(match[1])
		if n < 1 || n > 64 || match[2] != "5" || match[3] != "80" || match[4] != "0" || match[5] != "0" || match[6] != "0" || threads != 0 && threads != n {
			return 0, fmt.Errorf("encoder configuration gate: %s", engine)
		}
		threads = n
	}
	if engine == "vips" && threads != 1 {
		return 0, fmt.Errorf("vips encoder concurrency gate")
	}
	return threads, nil
}
