package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/parantail/content-serving-lab/internal/httpapi"
)

func TestHealthEndpoints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		body string
	}{
		{name: "live", path: "/health/live", body: "{\"status\":\"live\"}\n"},
		{name: "ready", path: "/health/ready", body: "{\"status\":\"ready\"}\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, tt.path, nil)

			httpapi.NewHandler().ServeHTTP(recorder, request)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
			}
			if got := recorder.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", got)
			}
			if got := recorder.Body.String(); got != tt.body {
				t.Fatalf("body = %q, want %q", got, tt.body)
			}
		})
	}
}

func TestHealthEndpointRejectsPost(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/health/ready", nil)

	httpapi.NewHandler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
	}
}
