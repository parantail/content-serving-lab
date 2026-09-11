package e3runner

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type fakePutter struct {
	keys  []string
	types []string
	fail  string
}

func (f *fakePutter) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if input.IfNoneMatch == nil || *input.IfNoneMatch != "*" {
		return nil, errors.New("upload must be conditional")
	}
	if _, err := io.ReadAll(input.Body); err != nil {
		return nil, err
	}
	if *input.Key == f.fail {
		return nil, errors.New("simulated conflict")
	}
	f.keys = append(f.keys, *input.Key)
	f.types = append(f.types, *input.ContentType)
	return &s3.PutObjectOutput{}, nil
}

func TestUploadDirectoryUploadsEveryFileConditionally(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	for _, name := range []string{"run.json", "requests.csv", "analysis/analysis.json", "analysis/timeline-none.svg", ".hidden"} {
		path := filepath.Join(directory, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	putter := &fakePutter{}
	uploaded, err := UploadDirectory(context.Background(), putter, "bucket", "/results/run-1/", directory)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(putter.keys)
	want := []string{"results/run-1/analysis/analysis.json", "results/run-1/analysis/timeline-none.svg", "results/run-1/requests.csv", "results/run-1/run.json"}
	if uploaded != 4 || len(putter.keys) != 4 {
		t.Fatalf("uploaded = %d keys = %v", uploaded, putter.keys)
	}
	for index, key := range want {
		if putter.keys[index] != key {
			t.Fatalf("keys = %v, want %v", putter.keys, want)
		}
	}
	if contentTypeFor("a.svg") != "image/svg+xml" || contentTypeFor("b.csv") != "text/csv" || contentTypeFor("c.prom") != "text/plain" {
		t.Fatal("unexpected content types")
	}

	failing := &fakePutter{fail: "results/run-1/run.json"}
	if _, err := UploadDirectory(context.Background(), failing, "bucket", "results/run-1", directory); err == nil {
		t.Fatal("conflict was not reported")
	}
	if _, err := UploadDirectory(context.Background(), nil, "bucket", "results", directory); err == nil {
		t.Fatal("nil client accepted")
	}
}
