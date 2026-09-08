package e1awss4

import (
	"context"
	"crypto/sha256"
	"os"
	"syscall"
	"testing"
	"time"
)

// Opt-in diagnostic: run the compiled test as the only process in a 1-vCPU
// container. It reports observations, not a Fargate performance guarantee.
func TestResourceCalibration(t *testing.T) {
	if os.Getenv("E1_RESOURCE_CALIBRATION") != "true" {
		t.Skip("set E1_RESOURCE_CALIBRATION=true in an isolated container")
	}
	source, err := NewCgroupSource("/sys/fs/cgroup")
	if err != nil {
		t.Fatal(err)
	}
	processCPU := func() time.Duration {
		var usage syscall.Rusage
		if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
			t.Fatal(err)
		}
		return time.Duration(usage.Utime.Nano() + usage.Stime.Nano())
	}
	for repetition := 1; repetition <= 3; repetition++ {
		// Reverse order to expose ordering/warmup effects; never select best runs.
		modes := []int{0, 1, 2, 3}
		if repetition%2 == 0 {
			modes = []int{3, 2, 1, 0}
		}
		for _, mode := range modes {
			busy, sampling := mode >= 2, mode%2 == 1
			before, err := source.Sample(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			cpuBefore := processCPU()
			started := time.Now()
			var sampler *resourceSampler
			if sampling {
				sampler, err = startResourceSampler(context.Background(), source, 50*time.Millisecond)
				if err != nil {
					t.Fatal(err)
				}
			}
			deadline := time.Now().Add(time.Second)
			iterations := uint64(0)
			value := [32]byte{1}
			if busy {
				for time.Now().Before(deadline) {
					value = sha256.Sum256(value[:])
					iterations++
				}
			} else {
				time.Sleep(time.Until(deadline))
			}
			samples := 0
			if sampler != nil {
				for _, sample := range sampler.finish() {
					if sample.Error != "" {
						t.Fatal(sample.Error)
					}
					samples++
				}
			}
			cpuDelta := processCPU() - cpuBefore
			after, err := source.Sample(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if after.CPUUsageNanos < before.CPUUsageNanos {
				t.Fatal("cgroup CPU moved backwards")
			}
			if busy && (cpuDelta <= 0 || after.CPUUsageNanos == before.CPUUsageNanos) {
				t.Fatal("busy workload did not advance both CPU counters")
			}
			t.Logf("source=%s repetition=%d busy=%t sampling=%t wall_ms=%.3f cgroup_cpu_ms=%.3f process_cpu_ms=%.3f samples=%d iterations=%d checksum=%x",
				source.Name(), repetition, busy, sampling, float64(time.Since(started))/1e6,
				float64(after.CPUUsageNanos-before.CPUUsageNanos)/1e6, float64(cpuDelta)/1e6, samples, iterations, value[:4])
		}
	}
}
