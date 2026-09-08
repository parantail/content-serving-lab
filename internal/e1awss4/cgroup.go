package e1awss4

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// CgroupSource reads kernel counters in the container-visible cgroup mounts.
// It never substitutes cached metadata stats or process RSS for cgroup usage.
type CgroupSource struct {
	cpuPath, memoryPath string
	v2                  bool
}

func NewCgroupSource(root string) (*CgroupSource, error) {
	if _, err := os.Stat(filepath.Join(root, "cgroup.controllers")); err == nil {
		source := &CgroupSource{cpuPath: filepath.Join(root, "cpu.stat"), memoryPath: filepath.Join(root, "memory.current"), v2: true}
		_, err := source.Sample(context.Background())
		return source, err
	}
	for _, mount := range []string{"cpuacct", "cpu,cpuacct", "cpuacct,cpu"} {
		path := filepath.Join(root, mount, "cpuacct.usage")
		if _, err := os.Stat(path); err == nil {
			source := &CgroupSource{cpuPath: path, memoryPath: filepath.Join(root, "memory", "memory.usage_in_bytes")}
			_, err := source.Sample(context.Background())
			return source, err
		}
	}
	return nil, errors.New("supported container cgroup CPU and memory counters are unavailable")
}

func (s *CgroupSource) Name() string {
	if s.v2 {
		return "cgroup-v2-container-visible"
	}
	return "cgroup-v1-container-visible"
}

func (s *CgroupSource) Sample(ctx context.Context) (ResourcePoint, error) {
	if err := ctx.Err(); err != nil {
		return ResourcePoint{}, err
	}
	cpu, err := os.ReadFile(s.cpuPath)
	if err != nil {
		return ResourcePoint{}, errors.New("read cgroup CPU counter failed")
	}
	var cpuNanos uint64
	if s.v2 {
		found := false
		for _, line := range strings.Split(string(cpu), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 && fields[0] == "usage_usec" {
				value, parseErr := strconv.ParseUint(fields[1], 10, 64)
				if parseErr != nil || value > math.MaxUint64/1000 {
					return ResourcePoint{}, errors.New("invalid cgroup CPU microseconds")
				}
				cpuNanos, found = value*1000, true
				break
			}
		}
		if !found {
			return ResourcePoint{}, errors.New("cgroup CPU stats has no usage_usec")
		}
	} else {
		cpuNanos, err = strconv.ParseUint(strings.TrimSpace(string(cpu)), 10, 64)
		if err != nil {
			return ResourcePoint{}, errors.New("invalid cgroup CPU nanoseconds")
		}
	}
	memory, err := os.ReadFile(s.memoryPath)
	if err != nil {
		return ResourcePoint{}, errors.New("read cgroup memory counter failed")
	}
	memoryBytes, err := strconv.ParseUint(strings.TrimSpace(string(memory)), 10, 64)
	if err != nil {
		return ResourcePoint{}, fmt.Errorf("invalid cgroup memory usage")
	}
	return ResourcePoint{CPUUsageNanos: cpuNanos, MemoryUsageBytes: memoryBytes}, nil
}
