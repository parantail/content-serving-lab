package e1awss4

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ScopeDiagnostic contains comparisons and counts only, never raw mount paths,
// process command lines, container IDs or host identifiers.
type ScopeDiagnostic struct {
	Controller                string `json:"controller"`
	PathAliasResolved         bool   `json:"path_alias_resolved"`
	MountFound                bool   `json:"mount_found"`
	MembershipFound           bool   `json:"membership_found"`
	MembershipIsRoot          bool   `json:"membership_is_root"`
	MembershipEqualsMountRoot bool   `json:"membership_equals_mount_root"`
	MountRootIsRoot           bool   `json:"mount_root_is_root"`
	SelfDirectMember          bool   `json:"self_direct_member"`
	VisibleDirectMembers      int    `json:"visible_direct_members"`
	HiddenDirectMembers       int    `json:"hidden_direct_members"`
	ChildGroups               int    `json:"child_groups"`
}

func describeScope(directory, controller, memberships, mounts string, pid int) (ScopeDiagnostic, error) {
	scope := ScopeDiagnostic{Controller: controller}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return scope, errors.New("resolve diagnostic cgroup path failed")
	}
	scope.PathAliasResolved = resolved != filepath.Clean(directory)
	directory = resolved
	member := ""
	for _, line := range strings.Split(memberships, "\n") {
		fields := strings.SplitN(line, ":", 3)
		if len(fields) == 3 && (controller == "unified" && fields[1] == "" || containsController(fields[1], controller)) {
			member, scope.MembershipFound = fields[2], true
		}
	}
	scope.MembershipIsRoot = scope.MembershipFound && member == "/"
	best := 0
	for _, line := range strings.Split(mounts, "\n") {
		parts := strings.SplitN(line, " - ", 2)
		if len(parts) != 2 {
			continue
		}
		fields, filesystem := strings.Fields(parts[0]), strings.Fields(parts[1])
		if len(fields) < 6 || len(filesystem) < 3 {
			continue
		}
		if controller == "unified" {
			if filesystem[0] != "cgroup2" {
				continue
			}
		} else if filesystem[0] != "cgroup" || !containsController(filesystem[2], controller) {
			continue
		}
		mount := decodeMountInfoPath(fields[4])
		mountRoot := decodeMountInfoPath(fields[3])
		if (directory == mount || strings.HasPrefix(directory, strings.TrimSuffix(mount, "/")+"/")) && len(mount) > best {
			best = len(mount)
			scope.MountFound = true
			scope.MountRootIsRoot = mountRoot == "/"
			scope.MembershipEqualsMountRoot = scope.MembershipFound && mountRoot == member
		}
	}
	data, err := os.ReadFile(filepath.Join(directory, "cgroup.procs"))
	if err != nil {
		return scope, errors.New("read diagnostic cgroup membership failed")
	}
	seen := map[int]bool{}
	for _, field := range strings.Fields(string(data)) {
		value, err := strconv.Atoi(field)
		if err != nil || value < 0 {
			return scope, errors.New("invalid diagnostic cgroup membership")
		}
		if value == 0 {
			scope.HiddenDirectMembers++
			continue
		}
		seen[value] = true
		if value == pid {
			scope.SelfDirectMember = true
		}
	}
	scope.VisibleDirectMembers = len(seen)
	entries, err := os.ReadDir(directory)
	if err != nil {
		return scope, errors.New("list diagnostic cgroup children failed")
	}
	for _, entry := range entries {
		if entry.IsDir() {
			scope.ChildGroups++
		}
	}
	return scope, nil
}

func decodeMountInfoPath(path string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(path)
}

func containsController(list, controller string) bool {
	for _, item := range strings.Split(list, ",") {
		if item == controller {
			return true
		}
	}
	return false
}

// RunResourceDiagnostic runs a bounded, opt-in synthetic diagnostic. It never
// opens the HTTP server, changes cgroups, or writes to AWS services directly.
func RunResourceDiagnostic(ctx context.Context, writer io.Writer, commit string) error {
	source, err := NewCgroupSource("/sys/fs/cgroup")
	if err != nil {
		return err
	}
	memberships, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return errors.New("read diagnostic process membership failed")
	}
	mounts, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return errors.New("read diagnostic mount info failed")
	}
	encoder := json.NewEncoder(writer)
	emitScope := func(stage string) error {
		controllers := []string{"cpuacct", "memory"}
		if source.v2 {
			controllers = []string{"unified", "unified"}
		}
		for i, path := range []string{source.cpuPath, source.memoryPath} {
			scope, err := describeScope(filepath.Dir(path), controllers[i], string(memberships), string(mounts), os.Getpid())
			if err != nil {
				return err
			}
			if err := encoder.Encode(map[string]any{"kind": "scope", "stage": stage, "source": source.Name(), "counter": []string{"cpu", "memory"}[i], "scope": scope}); err != nil {
				return err
			}
		}
		return nil
	}
	if err := emitScope("before"); err != nil {
		return err
	}
	processCPU := func() (int64, error) {
		var usage syscall.Rusage
		if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
			return 0, err
		}
		return usage.Utime.Nano() + usage.Stime.Nano(), nil
	}
	for repetition := 1; repetition <= 3; repetition++ {
		modes := []int{0, 1, 2, 3}
		if repetition%2 == 0 {
			modes = []int{3, 2, 1, 0}
		}
		for _, mode := range modes {
			if err := ctx.Err(); err != nil {
				return err
			}
			before, err := source.Sample(ctx)
			if err != nil {
				return err
			}
			cpuBefore, err := processCPU()
			if err != nil {
				return err
			}
			started := time.Now()
			busy, sampling := mode >= 2, mode%2 == 1
			var sampler *resourceSampler
			if sampling {
				sampler, err = startResourceSampler(ctx, source, 50*time.Millisecond)
				if err != nil {
					return err
				}
			}
			deadline := time.Now().Add(time.Second)
			iterations := uint64(0)
			value := [32]byte{1}
			if busy {
				for time.Now().Before(deadline) && ctx.Err() == nil {
					value = sha256.Sum256(value[:])
					iterations++
				}
			} else {
				timer := time.NewTimer(time.Until(deadline))
				select {
				case <-timer.C:
				case <-ctx.Done():
				}
				timer.Stop()
			}
			samples := 0
			if sampler != nil {
				for _, sample := range sampler.finish() {
					if sample.Error != "" {
						return errors.New("diagnostic sampling failed")
					}
					samples++
				}
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			cpuAfter, err := processCPU()
			if err != nil {
				return err
			}
			after, err := source.Sample(ctx)
			if err != nil {
				return err
			}
			if after.CPUUsageNanos < before.CPUUsageNanos || cpuAfter < cpuBefore {
				return errors.New("diagnostic CPU counter moved backwards")
			}
			if busy && (after.CPUUsageNanos == before.CPUUsageNanos || cpuAfter == cpuBefore) {
				return errors.New("diagnostic busy CPU did not advance")
			}
			if err := encoder.Encode(map[string]any{"kind": "window", "source": source.Name(), "repetition": repetition, "busy": busy, "sampling": sampling, "wall_ms": float64(time.Since(started)) / 1e6, "cgroup_cpu_ms": float64(after.CPUUsageNanos-before.CPUUsageNanos) / 1e6, "process_cpu_ms": float64(cpuAfter-cpuBefore) / 1e6, "samples": samples, "iterations": iterations, "checksum": value[0]}); err != nil {
				return err
			}
		}
	}
	if err := emitScope("after"); err != nil {
		return err
	}
	return encoder.Encode(map[string]any{"kind": "complete", "git_commit": commit, "windows": 12, "sample_gap_ms": 50, "source": source.Name()})
}
