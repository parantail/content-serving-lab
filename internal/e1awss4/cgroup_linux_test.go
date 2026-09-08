package e1awss4

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"
)

func TestLiveCgroupCPUAdvancesUnderLoad(t *testing.T) {
	source, err := NewCgroupSource("/sys/fs/cgroup")
	if err != nil {
		t.Skipf("container cgroup counters unavailable: %v", err)
	}
	before, err := source.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(150 * time.Millisecond)
	value := [32]byte{1}
	for time.Now().Before(deadline) {
		value = sha256.Sum256(value[:])
	}
	after, err := source.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after.CPUUsageNanos <= before.CPUUsageNanos {
		t.Fatalf("CPU did not advance: before=%d after=%d", before.CPUUsageNanos, after.CPUUsageNanos)
	}
	t.Logf("source=%s CPU delta=%dns memory=%d bytes checksum=%x", source.Name(), after.CPUUsageNanos-before.CPUUsageNanos, after.MemoryUsageBytes, value[:4])
}
