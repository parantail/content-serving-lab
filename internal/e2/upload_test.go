package e2

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestImmutableBatchUploads(t *testing.T) {
	var mu sync.Mutex
	puts := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method != "PUT" || r.Header.Get("If-None-Match") != "*" {
			t.Error("unconditional upload")
			w.WriteHeader(400)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		if puts[r.URL.Path] > 0 {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		puts[r.URL.Path]++
		w.Header().Set("ETag", `"test"`)
		w.WriteHeader(200)
	}))
	defer server.Close()
	root := t.TempDir()
	client := s3.New(s3.Options{Region: "us-east-1", BaseEndpoint: aws.String(server.URL), UsePathStyle: true, Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}, nil
	})})
	upload := &Uploader{client: client, bucket: "test", prefix: "run", root: root, sent: map[string]bool{}}
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := upload.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := upload.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "batch.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := upload.Flush(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	count := len(puts)
	mu.Unlock()
	if count != 2 {
		t.Fatalf("expected two immutable uploads, got %d", count)
	}
	upload.sent = map[string]bool{}
	if err := upload.Flush(); err == nil {
		t.Fatal("reused prefix was accepted")
	}
}
