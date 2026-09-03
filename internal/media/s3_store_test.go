package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const testDerivativeKey = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestS3DerivativeStorePutIfAbsentSendsConditionalRequest(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPut {
			t.Errorf("method = %q, want PUT", r.Method)
		}
		if r.URL.Path != "/derivative-bucket/derivatives/"+testDerivativeKey+".webp" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("If-None-Match"); got != "*" {
			t.Errorf("If-None-Match = %q, want *", got)
		}
		if got := r.Header.Get("Content-Type"); got != "image/webp" {
			t.Errorf("Content-Type = %q, want image/webp", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		if string(body) != "complete-webp" {
			t.Errorf("body = %q", body)
		}
		w.Header().Set("ETag", `"created"`)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	store := newTestS3DerivativeStore(t, server.URL)
	created, err := store.PutIfAbsent(context.Background(), testDerivativeKey, []byte("complete-webp"))
	if err != nil || !created {
		t.Fatalf("PutIfAbsent() = %v, %v; want true, nil", created, err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
}

func TestS3DerivativeStorePutIfAbsentTreats412AsExisting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeS3Error(w, http.StatusPreconditionFailed, "PreconditionFailed")
	}))
	defer server.Close()

	store := newTestS3DerivativeStore(t, server.URL)
	created, err := store.PutIfAbsent(context.Background(), testDerivativeKey, []byte("losing-webp"))
	if err != nil || created {
		t.Fatalf("PutIfAbsent() = %v, %v; want false, nil", created, err)
	}
}

func TestS3DerivativeStoreRetries409ThenCreates(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			writeS3Error(w, http.StatusConflict, "ConditionalRequestConflict")
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	store := newTestS3DerivativeStore(t, server.URL)
	created, err := store.PutIfAbsent(context.Background(), testDerivativeKey, []byte("complete-webp"))
	if err != nil || !created {
		t.Fatalf("PutIfAbsent() = %v, %v; want true, nil", created, err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}
}

func TestS3DerivativeStoreReturnsConflictAfterLimitedRetries(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeS3Error(w, http.StatusConflict, "ConditionalRequestConflict")
	}))
	defer server.Close()

	store := newTestS3DerivativeStore(t, server.URL)
	created, err := store.PutIfAbsent(context.Background(), testDerivativeKey, []byte("complete-webp"))
	if created || !errors.Is(err, ErrS3ConditionalWriteConflict) {
		t.Fatalf("PutIfAbsent() = %v, %v; want false, ErrS3ConditionalWriteConflict", created, err)
	}
	if got, want := requests.Load(), int64(DefaultS3ConflictRetries+1); got != want {
		t.Fatalf("requests = %d, want %d", got, want)
	}
}

func TestS3DerivativeStoreGetDistinguishesHitMissAndError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantFound  bool
		wantError  bool
	}{
		{name: "hit", statusCode: http.StatusOK, body: "stored-webp", wantFound: true},
		{name: "missing", statusCode: http.StatusNotFound},
		{name: "access denied is not a miss", statusCode: http.StatusForbidden, wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("method = %q, want GET", r.Method)
				}
				if r.URL.Path != "/derivative-bucket/derivatives/"+testDerivativeKey+".webp" {
					t.Errorf("path = %q", r.URL.Path)
				}
				if tt.statusCode != http.StatusOK {
					code := "NoSuchKey"
					if tt.statusCode == http.StatusForbidden {
						code = "AccessDenied"
					}
					writeS3Error(w, tt.statusCode, code)
					return
				}
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()

			store := newTestS3DerivativeStore(t, server.URL)
			data, found, err := store.Get(context.Background(), testDerivativeKey)
			if found != tt.wantFound || (err != nil) != tt.wantError {
				t.Fatalf("Get() = %q, %v, %v", data, found, err)
			}
			if found && string(data) != tt.body {
				t.Fatalf("Get() data = %q, want %q", data, tt.body)
			}
		})
	}
}

func TestS3OriginalStoreReadsHashKeyAndMaps404(t *testing.T) {
	tests := []struct {
		name      string
		found     bool
		wantError error
	}{
		{name: "found", found: true},
		{name: "missing", wantError: ErrOriginalNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/original-bucket/originals/"+testSourceHash {
					t.Errorf("path = %q", r.URL.Path)
				}
				if !tt.found {
					writeS3Error(w, http.StatusNotFound, "NoSuchKey")
					return
				}
				_, _ = io.WriteString(w, "original-jpeg")
			}))
			defer server.Close()

			store, err := NewS3OriginalStore(newTestS3Client(server.URL), "original-bucket")
			if err != nil {
				t.Fatal(err)
			}
			data, err := store.Read(context.Background(), testSourceHash)
			if !errors.Is(err, tt.wantError) {
				t.Fatalf("Read() error = %v, want %v", err, tt.wantError)
			}
			if tt.found && string(data) != "original-jpeg" {
				t.Fatalf("Read() data = %q", data)
			}
		})
	}
}

func TestS3StoresRecordTaskLevelRequestOutcomesAndBytes(t *testing.T) {
	var derivativeGets atomic.Int64
	var originalGets atomic.Int64
	var puts atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/derivative-bucket/"):
			switch derivativeGets.Add(1) {
			case 1:
				_, _ = io.WriteString(w, "winner")
			case 2:
				writeS3Error(w, http.StatusNotFound, "NoSuchKey")
			default:
				writeS3Error(w, http.StatusForbidden, "AccessDenied")
			}
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/original-bucket/"):
			if originalGets.Add(1) == 1 {
				_, _ = io.WriteString(w, "original")
				return
			}
			writeS3Error(w, http.StatusForbidden, "AccessDenied")
		case r.Method == http.MethodPut:
			switch puts.Add(1) {
			case 1:
				writeS3Error(w, http.StatusConflict, "ConditionalRequestConflict")
			case 2:
				writeS3Error(w, http.StatusPreconditionFailed, "PreconditionFailed")
			case 3:
				writeS3Error(w, http.StatusInternalServerError, "InternalError")
			default:
				w.WriteHeader(http.StatusOK)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	metrics := &S3Metrics{}
	derivatives, err := NewS3DerivativeStore(newTestS3Client(server.URL), "derivative-bucket", metrics)
	if err != nil {
		t.Fatal(err)
	}
	originals, err := NewS3OriginalStore(newTestS3Client(server.URL), "original-bucket", metrics)
	if err != nil {
		t.Fatal(err)
	}

	if data, found, err := derivatives.Get(context.Background(), testDerivativeKey); err != nil || !found || string(data) != "winner" {
		t.Fatalf("derivative hit = %q, %v, %v", data, found, err)
	}
	if _, found, err := derivatives.Get(context.Background(), testDerivativeKey); err != nil || found {
		t.Fatalf("derivative miss = %v, %v", found, err)
	}
	if _, _, err := derivatives.Get(context.Background(), testDerivativeKey); err == nil {
		t.Fatal("derivative access error = nil")
	}
	if data, err := originals.Read(context.Background(), testSourceHash); err != nil || string(data) != "original" {
		t.Fatalf("original hit = %q, %v", data, err)
	}
	if _, err := originals.Read(context.Background(), testSourceHash); err == nil {
		t.Fatal("original access error = nil")
	}
	if created, err := derivatives.PutIfAbsent(context.Background(), testDerivativeKey, []byte("candidate")); err != nil || created {
		t.Fatalf("conflict then existing = %v, %v", created, err)
	}
	if _, err := derivatives.PutIfAbsent(context.Background(), testDerivativeKey, []byte("bad")); err == nil {
		t.Fatal("publish error = nil")
	}
	if created, err := derivatives.PutIfAbsent(context.Background(), testDerivativeKey, []byte("created")); err != nil || !created {
		t.Fatalf("publish created = %v, %v", created, err)
	}

	want := S3MetricsSnapshot{
		DerivativeGetHit:    1,
		DerivativeGetMiss:   1,
		DerivativeGetError:  1,
		DerivativeGetBytes:  int64(len("winner")),
		OriginalGetSuccess:  1,
		OriginalGetError:    1,
		OriginalGetBytes:    int64(len("original")),
		PublishCreated:      1,
		PublishExisting:     1,
		PublishConflict:     1,
		PublishError:        1,
		PublishAttemptBytes: int64(2*len("candidate") + len("bad") + len("created")),
	}
	if got := metrics.Snapshot(); got != want {
		t.Fatalf("S3 metrics = %+v, want %+v", got, want)
	}
}

func newTestS3DerivativeStore(t *testing.T, endpoint string) *S3DerivativeStore {
	t.Helper()
	store, err := NewS3DerivativeStore(newTestS3Client(endpoint), "derivative-bucket")
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func newTestS3Client(endpoint string) *s3.Client {
	return s3.New(s3.Options{
		BaseEndpoint: aws.String(endpoint),
		Credentials:  aws.AnonymousCredentials{},
		Region:       "ap-northeast-2",
		Retryer:      aws.NopRetryer{},
		UsePathStyle: true,
	})
}

func writeS3Error(w http.ResponseWriter, statusCode int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(statusCode)
	_, _ = fmt.Fprintf(w, `<Error><Code>%s</Code><Message>%s</Message></Error>`, code, strings.ToLower(code))
}
