package e1awss4

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCgroupCountersReadFreshKernelValues(t *testing.T) {
	for _, version := range []string{"v1", "v2"} {
		t.Run(version, func(t *testing.T) {
			root := t.TempDir()
			put := func(name, data string) {
				t.Helper()
				path := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(data), 0644); err != nil {
					t.Fatal(err)
				}
			}
			cpuPath, memPath := "cpuacct/cpuacct.usage", "memory/memory.usage_in_bytes"
			initialCPU, nextCPU := "100000", "900000"
			if version == "v2" {
				put("cgroup.controllers", "cpu memory")
				cpuPath, memPath = "cpu.stat", "memory.current"
				initialCPU, nextCPU = "usage_usec 100\nuser_usec 90\n", "usage_usec 900\nuser_usec 800\n"
			}
			put(cpuPath, initialCPU)
			put(memPath, "4096")
			source, err := NewCgroupSource(root)
			if err != nil {
				t.Fatal(err)
			}
			first, err := source.Sample(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			put(cpuPath, nextCPU)
			put(memPath, "8192")
			last, err := source.Sample(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if last.CPUUsageNanos-first.CPUUsageNanos != 800000 || last.MemoryUsageBytes != 8192 {
				t.Fatalf("points: %+v %+v", first, last)
			}
			put(cpuPath, "invalid")
			if _, err := source.Sample(context.Background()); err == nil {
				t.Fatal("invalid counter silently accepted")
			}
			if err := os.Remove(filepath.Join(root, memPath)); err != nil {
				t.Fatal(err)
			}
			put(cpuPath, initialCPU)
			if _, err := source.Sample(context.Background()); err == nil {
				t.Fatal("missing memory silently accepted")
			}
		})
	}
}

func TestCgroupUnavailableDoesNotFallBackToMetadata(t *testing.T) {
	if _, err := NewCgroupSource(t.TempDir()); err == nil {
		t.Fatal("expected unavailable cgroup error")
	}
}
