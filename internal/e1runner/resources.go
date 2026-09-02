package e1runner

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type resourceSampler struct {
	runID   string
	trialID string
	gap     time.Duration
	start   time.Time
	stop    chan struct{}
	done    chan struct{}

	mu      sync.Mutex
	samples []ResourceSample
}

func startResourceSampler(runID, trialID string, gap time.Duration) *resourceSampler {
	sampler := &resourceSampler{
		runID:   runID,
		trialID: trialID,
		gap:     gap,
		start:   time.Now(),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	go sampler.run()
	return sampler
}

func (s *resourceSampler) run() {
	defer close(s.done)
	s.capture()
	ticker := time.NewTicker(s.gap)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.capture()
		case <-s.stop:
			s.capture()
			return
		}
	}
}

func (s *resourceSampler) capture() {
	now := time.Now()
	sample := ResourceSample{
		RunID:             s.runID,
		TrialID:           s.trialID,
		Timestamp:         now.UTC().Format(time.RFC3339Nano),
		ElapsedMS:         milliseconds(now.Sub(s.start)),
		CPUUsageUsec:      readCPUUsageUsec(),
		RSSBytes:          readRSSBytes(),
		CgroupMemoryBytes: readIntFile("/sys/fs/cgroup/memory.current"),
	}
	s.mu.Lock()
	s.samples = append(s.samples, sample)
	s.mu.Unlock()
}

func (s *resourceSampler) finish() []ResourceSample {
	close(s.stop)
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ResourceSample(nil), s.samples...)
}

func readCPUUsageUsec() int64 {
	file, err := os.Open("/sys/fs/cgroup/cpu.stat")
	if err != nil {
		return -1
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[0] == "usage_usec" {
			value, err := strconv.ParseInt(fields[1], 10, 64)
			if err == nil {
				return value
			}
		}
	}
	return -1
}

func readRSSBytes() int64 {
	file, err := os.Open("/proc/self/status")
	if err != nil {
		return -1
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == "VmRSS:" {
			value, err := strconv.ParseInt(fields[1], 10, 64)
			if err == nil {
				return value * 1024
			}
		}
	}
	return -1
}

func readIntFile(path string) int64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return -1
	}
	value, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return -1
	}
	return value
}

func readCPUQuota() string {
	data, err := os.ReadFile("/sys/fs/cgroup/cpu.max")
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(data))
}

func readMemoryLimit() int64 {
	return readIntFile("/sys/fs/cgroup/memory.max")
}
