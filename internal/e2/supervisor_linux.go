//go:build linux

package e2

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/parantail/content-serving-lab/internal/e1awss4"
)

type Job struct {
	Engine string `json:"engine"`
	Batch  Batch  `json:"batch"`
}

// Matrix is fixed before any transform executes. The seed is paired across engines.
func Matrix(mode, root, output string) ([]Job, error) {
	if mode != "validate" && mode != "quality" && mode != "calibrate" && mode != "measure" && mode != "diagnose" {
		return nil, fmt.Errorf("invalid mode")
	}
	var jobs []Job
	reps := 1
	if mode == "measure" {
		reps = 5
	}
	ops := []string{"resize", "contain", "cover"}
	formats := []string{"jpeg", "png", "webp", "avif"}
	concurrencies := []int{1}
	qualities := []int{80}
	if mode == "measure" || mode == "calibrate" {
		concurrencies = []int{1, 4}
	}
	if mode == "quality" {
		ops = []string{"cover"}
		formats = []string{"jpeg", "webp", "avif"}
		qualities = []int{50, 65, 80, 90}
	}
	if mode == "diagnose" {
		ops = []string{"cover"}
		formats = []string{"avif"}
	}
	for rep := 0; rep < reps; rep++ {
		for _, op := range ops {
			for _, format := range formats {
				for _, c := range concurrencies {
					for _, q := range qualities {
						engines := []string{"vips", "magick"}
						if rep%2 == 1 {
							engines = []string{"magick", "vips"}
						}
						for _, engine := range engines {
							id := fmt.Sprintf("%s-%s-%s-q%d-c%d-r%d", engine, op, format, q, c, rep)
							jobs = append(jobs, Job{engine, Batch{ID: id, Root: root, Output: filepath.Join(output, "outputs", id), Spec: Spec{op, format, q}, Concurrency: c, Repetition: rep, Seed: 209 + int64(rep), Quality: mode == "quality" || mode == "diagnose", Save: mode == "validate" || mode == "quality", Warmup: mode == "measure" || mode == "calibrate"}})
						}
					}
				}
			}
		}
	}
	return jobs, nil
}

type BatchResult struct {
	Schema             string         `json:"schema"`
	Job                Job            `json:"job"`
	Started            string         `json:"started"`
	WallNS             int64          `json:"wall_ns"`
	ExitCode           int            `json:"exit_code"`
	Error              string         `json:"error,omitempty"`
	WaitRSSBytes       int64          `json:"wait_rss_bytes"`
	SelfRSSBytes       int64          `json:"self_rss_bytes"`
	WaitCPUUserNS      int64          `json:"wait_cpu_user_ns"`
	WaitCPUSystemNS    int64          `json:"wait_cpu_system_ns"`
	CgroupSource       string         `json:"cgroup_source"`
	CgroupPeakBytes    uint64         `json:"cgroup_sampled_peak_bytes"`
	CgroupCPUNS        uint64         `json:"cgroup_cpu_ns"`
	SampleCount        int            `json:"sample_count"`
	Scope              map[string]any `json:"scope,omitempty"`
	Terminated         []Event        `json:"terminated,omitempty"`
	MemoryEventsBefore string         `json:"memory_events_before,omitempty"`
	MemoryEventsAfter  string         `json:"memory_events_after,omitempty"`
	Completed          int            `json:"completed"`
	WarmCompleted      int            `json:"warm_completed"`
	WorkerPeakThreads  int            `json:"worker_sampled_peak_threads"`
}

func WriteJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}

func optionalRead(path string) string { b, _ := os.ReadFile(path); return strings.TrimSpace(string(b)) }

// Record membership equality and visible process membership, without publishing host paths.
// This explicitly describes a parent + one child scope, not E1's self-only scope.
func Scope(child int) (map[string]any, error) {
	parentMembership, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return nil, err
	}
	childMembership, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", child))
	if err != nil {
		return nil, err
	}
	equal := bytes.Equal(parentMembership, childMembership)
	roots := []string{"/sys/fs/cgroup"}
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		roots = []string{"/sys/fs/cgroup/memory", "/sys/fs/cgroup/cpu,cpuacct", "/sys/fs/cgroup/cpuacct,cpu", "/sys/fs/cgroup/cpuacct"}
	}
	var controllers []map[string]any
	for _, root := range roots {
		data, err := os.ReadFile(filepath.Join(root, "cgroup.procs"))
		if err != nil {
			continue
		}
		parentFound, childFound, visible, hidden := false, false, 0, 0
		for _, s := range strings.Fields(string(data)) {
			pid, _ := strconv.Atoi(s)
			if pid == os.Getpid() {
				parentFound = true
			}
			if pid == child {
				childFound = true
			}
			if pid == 0 {
				hidden++
			} else {
				visible++
			}
		}
		controllers = append(controllers, map[string]any{"controller": filepath.Base(root), "parent_member": parentFound, "child_member": childFound, "visible_direct_members": visible, "hidden_direct_members": hidden})
		if !parentFound || !childFound {
			return nil, fmt.Errorf("cgroup parent/child membership gate")
		}
	}
	if !equal || len(controllers) == 0 {
		return nil, fmt.Errorf("cgroup scope gate")
	}
	limits := map[string]string{}
	for _, p := range []string{"cpu.max", "memory.max", "cpu/cpu.cfs_quota_us", "cpu/cpu.cfs_period_us", "cpu,cpuacct/cpu.cfs_quota_us", "cpu,cpuacct/cpu.cfs_period_us", "memory/memory.limit_in_bytes"} {
		if v := optionalRead(filepath.Join("/sys/fs/cgroup", p)); v != "" {
			limits[p] = v
		}
	}
	return map[string]any{"membership_equal": equal, "scope": "container parent and one worker; cgroup counters include both and file cache", "controllers": controllers, "limits": limits}, nil
}

// Supervise never relies on context cancellation to unwind C. A timeout kills the
// worker process group, waits for the child, and preserves each in-flight status.
func Supervise(ctx context.Context, job Job, binary, output string, trace bool, transformLimit time.Duration) (result BatchResult, returnErr error) {
	result = BatchResult{Schema: "e2-batch-v1", Job: job, Started: time.Now().UTC().Format(time.RFC3339Nano), ExitCode: -1}
	start := time.Now()
	prefix := filepath.Join(output, job.Batch.ID)
	defer func() {
		result.WallNS = time.Since(start).Nanoseconds()
		if returnErr != nil {
			result.Error = returnErr.Error()
		}
		if e := WriteJSON(prefix+".json", result); returnErr == nil && e != nil {
			returnErr = e
		}
	}()
	source, err := e1awss4.NewCgroupSource("/sys/fs/cgroup")
	if err != nil {
		return result, err
	}
	result.CgroupSource = source.Name()
	eventFile, err := os.Create(prefix + ".jsonl")
	if err != nil {
		return result, err
	}
	defer eventFile.Close()
	sampleFile, err := os.Create(prefix + ".cgroup.jsonl")
	if err != nil {
		return result, err
	}
	defer sampleFile.Close()
	stderr, err := os.Create(prefix + ".stderr.log")
	if err != nil {
		return result, err
	}
	defer stderr.Close()
	payload, _ := json.Marshal(job.Batch)
	cmd := exec.Command(binary)
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Stderr = stderr
	cmd.Env = append(os.Environ(), "GOMAXPROCS=1", "OMP_NUM_THREADS=1", "MAGICK_THREAD_LIMIT=1", "VIPS_CONCURRENCY=1")
	if trace {
		cmd.Env = append(cmd.Env, "LD_PRELOAD=/app/e2-codec-trace.so")
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return result, err
	}
	enc := json.NewEncoder(sampleFile)
	var firstCPU, lastCPU uint64
	sample := func() error {
		p, e := source.Sample(ctx)
		if e != nil {
			return e
		}
		if result.SampleCount == 0 {
			firstCPU = p.CPUUsageNanos
		}
		if p.CPUUsageNanos < lastCPU {
			return fmt.Errorf("cgroup CPU counter regressed")
		}
		lastCPU = p.CPUUsageNanos
		result.SampleCount++
		result.CgroupPeakBytes = max(result.CgroupPeakBytes, p.MemoryUsageBytes)
		threads := 0
		if cmd.Process != nil {
			for _, line := range strings.Split(optionalRead(fmt.Sprintf("/proc/%d/status", cmd.Process.Pid)), "\n") {
				fields := strings.Fields(line)
				if len(fields) == 2 && fields[0] == "Threads:" {
					threads, _ = strconv.Atoi(fields[1])
					break
				}
			}
		}
		result.WorkerPeakThreads = max(result.WorkerPeakThreads, threads)
		return enc.Encode(map[string]any{"time_ns": time.Now().UnixNano(), "cpu_ns": p.CPUUsageNanos, "memory_bytes": p.MemoryUsageBytes})
	}
	if err = sample(); err != nil {
		return result, err
	}
	result.MemoryEventsBefore = optionalRead("/sys/fs/cgroup/memory.events")
	if err = cmd.Start(); err != nil {
		return result, err
	}
	type received struct {
		event Event
		err   error
	}
	events := make(chan received)
	go func() {
		defer close(events)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			var e Event
			err := json.Unmarshal(scanner.Bytes(), &e)
			events <- received{e, err}
			if err != nil {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			events <- received{err: err}
		}
	}()
	active := map[string]Event{}
	writer := json.NewEncoder(eventFile)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	ready, summary := false, false
	killed := false
	stop := func(reason error) {
		if returnErr == nil {
			returnErr = reason
		}
		if !killed {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			killed = true
		}
	}
	ctxDone := ctx.Done()
	for events != nil {
		select {
		case <-ctxDone:
			stop(ctx.Err())
			ctxDone = nil
		case <-ticker.C:
			if !killed {
				if err = sample(); err != nil {
					stop(err)
				}
			}
			if !ready && time.Since(start) > 60*time.Second {
				stop(fmt.Errorf("worker startup timeout"))
			}
			for _, e := range active {
				if time.Now().UnixNano()-e.TimeNS > transformLimit.Nanoseconds() {
					stop(fmt.Errorf("transform timeout"))
					break
				}
			}
		case item, ok := <-events:
			if !ok {
				events = nil
				break
			}
			if item.err != nil {
				stop(item.err)
				continue
			}
			e := item.event
			if err = writer.Encode(e); err != nil {
				stop(err)
			}
			if e.Batch != job.Batch.ID {
				stop(fmt.Errorf("event batch mismatch"))
				continue
			}
			key := fmt.Sprintf("%t/%s", e.Warmup, e.ID)
			switch e.Type {
			case "ready":
				ready = true
				result.Scope, err = Scope(cmd.Process.Pid)
				if err != nil {
					stop(err)
				}
			case "start":
				if _, exists := active[key]; exists {
					stop(fmt.Errorf("duplicate start"))
				}
				active[key] = e
			case "finish":
				if _, exists := active[key]; !exists {
					stop(fmt.Errorf("finish without start"))
				}
				delete(active, key)
				if e.Warmup {
					result.WarmCompleted++
				} else {
					result.Completed++
				}
				if e.Error != "" {
					stop(fmt.Errorf("transform error: %s: %s", e.ID, e.Error))
				}
			case "summary":
				summary = true
				result.SelfRSSBytes = e.PeakRSSBytes
			}
		}
	}
	waitErr := cmd.Wait()
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
		if usage, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
			result.WaitRSSBytes = usage.Maxrss * 1024
			result.WaitCPUUserNS = usage.Utime.Nano()
			result.WaitCPUSystemNS = usage.Stime.Nano()
		}
	}
	for _, e := range active {
		e.Type = "terminated"
		e.Error = "interrupted"
		if time.Now().UnixNano()-e.TimeNS > transformLimit.Nanoseconds() {
			e.Error = "timeout"
		}
		result.Terminated = append(result.Terminated, e)
		_ = writer.Encode(e)
	}
	result.MemoryEventsAfter = optionalRead("/sys/fs/cgroup/memory.events")
	if ctx.Err() == nil {
		if e := sample(); e != nil && returnErr == nil {
			returnErr = e
		}
	}
	result.CgroupCPUNS = lastCPU - firstCPU
	if returnErr != nil {
		return result, returnErr
	}
	if waitErr != nil {
		return result, fmt.Errorf("worker exit: %w", waitErr)
	}
	expected := 24
	if job.Batch.Quality {
		expected = 8
	}
	if !ready || !summary || len(active) != 0 || result.Completed != expected || job.Batch.Warmup && result.WarmCompleted != expected {
		return result, fmt.Errorf("incomplete event protocol")
	}
	// A later allocation during final JSON emission can only increase the wait4 HWM.
	if result.SelfRSSBytes <= 0 || result.WaitRSSBytes < result.SelfRSSBytes {
		return result, fmt.Errorf("RSS cross-check failed")
	}
	return result, nil
}
