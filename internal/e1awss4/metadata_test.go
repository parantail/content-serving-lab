package e1awss4

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testImageDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestMetadataClientLoadsSanitizedTaskIdentityAndTaskStats(t *testing.T) {
	t.Parallel()

	const accountID = "123456789012"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/task":
			_, _ = fmt.Fprintf(w, `{
				"TaskARN":"arn:aws:ecs:ap-northeast-2:%s:task/e1-cluster/0123456789abcdef0123456789abcdef",
				"Family":"e1-media-service",
				"Revision":7,
				"AvailabilityZone":"ap-northeast-2a",
				"LaunchType":"FARGATE",
				"Limits":{"CPU":1,"Memory":2048},
				"Containers":[
					{"Name":"sidecar","ImageID":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
					{"Name":"media-service","ImageID":"%s"}
				]
			}`, accountID, testImageDigest)
		case "/task/stats":
			_, _ = w.Write([]byte(`{
				"container-a":{"cpu_stats":{"cpu_usage":{"total_usage":125}},"memory_stats":{"usage":1024}},
				"container-b":{"cpu_stats":{"cpu_usage":{"total_usage":75}},"memory_stats":{"usage":512}}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewMetadataClient(server.URL, "media-service", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	identity, err := client.LoadTaskIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantIdentity := TaskIdentity{
		TaskID:                 "0123456789abcdef0123456789abcdef",
		TaskDefinitionFamily:   "e1-media-service",
		TaskDefinitionRevision: "7",
		AvailabilityZone:       "ap-northeast-2a",
		LaunchType:             "FARGATE",
		CPUVCpu:                1,
		MemoryMiB:              2048,
		ImageDigest:            testImageDigest,
	}
	if identity != wantIdentity {
		t.Fatalf("identity = %+v, want %+v", identity, wantIdentity)
	}
	if formatted := fmt.Sprintf("%+v", identity); strings.Contains(formatted, accountID) || strings.Contains(formatted, "arn:aws") {
		t.Fatalf("sanitized identity leaked ARN or account ID: %s", formatted)
	}

	point, err := client.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := (ResourcePoint{CPUUsageNanos: 200, MemoryUsageBytes: 1536}); point != want {
		t.Fatalf("resource point = %+v, want %+v", point, want)
	}
}

func TestMetadataClientRejectsMissingMediaContainerDigest(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"TaskARN":"arn:aws:ecs:ap-northeast-2:123456789012:task/cluster/task-id",
			"Family":"e1-media-service",
			"Revision":"7",
			"AvailabilityZone":"ap-northeast-2a",
			"LaunchType":"FARGATE",
			"Limits":{"CPU":1,"Memory":2048},
			"Containers":[{"Name":"sidecar","ImageID":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]
		}`))
	}))
	defer server.Close()

	client, err := NewMetadataClient(server.URL, "media-service", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.LoadTaskIdentity(context.Background()); err == nil || !strings.Contains(err.Error(), "image digest") {
		t.Fatalf("LoadTaskIdentity() error = %v, want image digest error", err)
	}
}
