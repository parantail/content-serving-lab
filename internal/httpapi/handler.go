package httpapi

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/parantail/content-serving-lab/internal/media"
)

type healthResponse struct {
	Status string `json:"status"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func NewHandler(processor *media.Processor) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", health("live"))
	mux.HandleFunc("GET /health/ready", health("ready"))
	if processor != nil {
		mux.HandleFunc("GET /i/{source}/{transform}", derivative(processor))
		mux.HandleFunc("GET /metrics", metrics(processor.Metrics()))
	}
	return mux
}

func health(status string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(healthResponse{Status: status})
	}
}

func derivative(processor *media.Processor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		transform := r.PathValue("transform")
		rawSpec, format, ok := strings.Cut(transform, ".")
		if !ok || strings.Contains(format, ".") {
			writeError(w, http.StatusBadRequest, "transform path must end with one format extension")
			return
		}
		spec, err := media.ParseTransformSpec(rawSpec, format)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		result, err := processor.GetDerivative(r.Context(), strings.ToLower(r.PathValue("source")), spec)
		if result.Key != "" {
			w.Header().Set("X-Derivative-Key", result.Key)
			w.Header().Set("X-Media-Cache", result.Cache)
			w.Header().Set("X-Request-Coalesced", fmt.Sprintf("%t", result.Coalesced))
		}
		if err != nil {
			switch {
			case errors.Is(err, media.ErrInvalidSpec):
				writeError(w, http.StatusBadRequest, err.Error())
			case errors.Is(err, media.ErrOriginalNotFound):
				writeError(w, http.StatusNotFound, "original not found")
			case errors.Is(err, r.Context().Err()):
				writeError(w, http.StatusRequestTimeout, "request canceled or timed out")
			default:
				writeError(w, http.StatusInternalServerError, "derivative generation failed")
			}
			return
		}

		etag := sha256.Sum256(result.Data)
		w.Header().Set("Content-Type", "image/webp")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("ETag", fmt.Sprintf("\"%x\"", etag))
		w.Header().Set("X-Derivative-Key", result.Key)
		w.Header().Set("X-Media-Cache", result.Cache)
		w.Header().Set("X-Request-Coalesced", fmt.Sprintf("%t", result.Coalesced))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(result.Data)
	}
}

func metrics(values *media.Metrics) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		if err := values.WritePrometheus(w); err != nil {
			http.Error(w, "write metrics", http.StatusInternalServerError)
		}
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{Error: message})
}
