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
		filesystem := "cgroup cgroup rw," + controller
		if controller == "unified" {
			filesystem = "cgroup2 cgroup rw"
		}
		mounts := "1 0 0:1 /private-identifier " + root + " rw - " + filesystem + "\n"
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

func TestScopeDiagnosticResolvesControllerAlias(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "cpu,cpuacct")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "cgroup.procs"), []byte("17\n"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "cpuacct")
	if err := os.Symlink("cpu,cpuacct", alias); err != nil {
		t.Fatal(err)
	}
	scope, err := describeScope(alias, "cpuacct", "2:cpu,cpuacct:/private-identifier\n",
		"1 0 0:1 /private-identifier "+directory+" rw - cgroup cgroup rw,cpu,cpuacct\n", 17)
	if err != nil {
		t.Fatal(err)
	}
	if !scope.PathAliasResolved || !scope.MountFound || !scope.MembershipEqualsMountRoot || !scope.SelfDirectMember {
		t.Fatalf("alias scope=%+v", scope)
	}
}

func TestScopeDiagnosticMountMatching(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private space\\path")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cgroup.procs"), []byte("17\n"), 0600); err != nil {
		t.Fatal(err)
	}
	escape := strings.NewReplacer(`\`, `\134`, " ", `\040`)
	mount := func(path, fs string) string {
		return "1 0 0:1 /private\\040member " + escape.Replace(path) + " rw shared:1 - " + fs + "\n"
	}
	for _, tt := range []struct {
		name, mounts string
		found        bool
	}{
		{"escaped", mount(root, "cgroup cgroup rw,cpu,cpuacct"), true},
		{"wrong controller", mount(root, "cgroup cgroup rw,memory"), false},
		{"partial controller", mount(root, "cgroup cgroup rw,cpuacct_extra"), false},
		{"wrong version", mount(root, "cgroup2 cgroup rw"), false},
		{"prefix only", mount(strings.TrimSuffix(root, "path"), "cgroup cgroup rw,cpuacct"), false},
		{"longest mount", mount("/", "cgroup cgroup rw,cpuacct") + mount(root, "cgroup cgroup rw,cpuacct"), true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			scope, err := describeScope(root, "cpuacct", "2:cpuacct:/private member\n", tt.mounts, 17)
			if err != nil {
				t.Fatal(err)
			}
			if scope.MountFound != tt.found || scope.MembershipEqualsMountRoot != tt.found || scope.PathAliasResolved {
				t.Fatalf("scope=%+v", scope)
			}
		})
	}
}

func TestScopeDiagnosticBrokenAliasSanitizesError(t *testing.T) {
	alias := filepath.Join(t.TempDir(), "private-alias")
	if err := os.Symlink("private-missing", alias); err != nil {
		t.Fatal(err)
	}
	_, err := describeScope(alias, "cpuacct", "", "", 17)
	if err == nil || err.Error() != "resolve diagnostic cgroup path failed" {
		t.Fatalf("unexpected error: %v", err)
	}
}
