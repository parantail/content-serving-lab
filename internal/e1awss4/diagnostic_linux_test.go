package e1awss4

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScopeDiagnosticDoesNotExposePathsAndCountsHiddenMembers(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "cgroup.procs"), []byte("17\n17\n0\n23\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, controller := range []string{"cpuacct", "memory", "unified"} {
		membership := "2:cpu,cpuacct:/private-identifier\n3:memory:/private-identifier\n0::/private-identifier\n"
		mounts := "1 0 0:1 /private-identifier " + root + " rw - cgroup cgroup rw\n"
		scope, err := describeScope(root, controller, membership, mounts, 17)
		if err != nil {
			t.Fatal(err)
		}
		if !scope.MountFound || !scope.MembershipFound || !scope.MembershipEqualsMountRoot || !scope.SelfDirectMember || scope.VisibleDirectMembers != 2 || scope.HiddenDirectMembers != 1 || scope.ChildGroups != 1 {
			t.Fatalf("scope=%+v", scope)
		}
		data, err := json.Marshal(scope)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "private-identifier") || strings.Contains(string(data), root) {
			t.Fatal("raw path leaked")
		}
	}
}

func TestScopeDiagnosticDoesNotInferMissingMembership(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "cgroup.procs"), []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	scope, err := describeScope(root, "cpuacct", "", "", 17)
	if err != nil {
		t.Fatal(err)
	}
	if scope.MembershipFound || scope.MountFound || scope.SelfDirectMember || scope.MembershipEqualsMountRoot {
		t.Fatalf("invented membership: %+v", scope)
	}
}
