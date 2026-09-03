package httpapi

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/parantail/content-serving-lab/internal/e1awss4"
	"github.com/parantail/content-serving-lab/internal/media"
)

type healthResponse struct {
	Status string `json:"status"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func NewHandler(processor *media.Processor) http.Handler {
	return NewHandlerWithExperiment(processor, nil)
}

func NewHandlerWithExperiment(processor *media.Processor, experiment *e1awss4.Controller) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", health("live"))
	mux.HandleFunc("GET /health/ready", health("ready"))
	if processor != nil {
		mux.HandleFunc("GET /i/{source}/{transform}", derivative(processor, experiment))
		mux.HandleFunc("GET /metrics", metrics(processor.Metrics()))
	}
	if experiment != nil {
		mux.HandleFunc("POST /internal/e1/trials/{trial}/prepare", prepareExperiment(experiment))
		mux.HandleFunc("POST /internal/e1/trials/{trial}/finish", finishExperiment(experiment))
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

func derivative(processor *media.Processor, experiment *e1awss4.Controller) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if experiment != nil {
			trialID := r.Header.Get(e1awss4.TrialIDHeader)
			w.Header().Set(e1awss4.TaskIDHeader, experiment.Identity().TaskID)
			finishRequest, err := experiment.BeginRequest(trialID)
			if err != nil {
				writeExperimentError(w, err, false)
				return
			}
			w.Header().Set(e1awss4.TrialIDHeader, trialID)
			defer finishRequest()
		}

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

func prepareExperiment(experiment *e1awss4.Controller) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		trialID := r.PathValue("trial")
		setExperimentHeaders(w, experiment)
		report, err := experiment.Prepare(r.Context(), trialID)
		if err != nil {
			writeExperimentError(w, err, true)
			return
		}
		w.Header().Set(e1awss4.TrialIDHeader, trialID)
		writeJSON(w, http.StatusOK, report)
	}
}

func finishExperiment(experiment *e1awss4.Controller) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		trialID := r.PathValue("trial")
		setExperimentHeaders(w, experiment)
		report, err := experiment.Finish(trialID)
		if err != nil {
			writeExperimentError(w, err, false)
			return
		}
		w.Header().Set(e1awss4.TrialIDHeader, trialID)
		writeJSON(w, http.StatusOK, report)
	}
}

func setExperimentHeaders(w http.ResponseWriter, experiment *e1awss4.Controller) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set(e1awss4.TaskIDHeader, experiment.Identity().TaskID)
}

func writeExperimentError(w http.ResponseWriter, err error, preparing bool) {
	switch {
	case errors.Is(err, e1awss4.ErrInvalidTrialID):
		writeError(w, http.StatusBadRequest, "invalid experiment trial ID")
	case errors.Is(err, e1awss4.ErrTrialConflict), errors.Is(err, e1awss4.ErrTrialNotPrepared), errors.Is(err, e1awss4.ErrTrialNotDrained), errors.Is(err, e1awss4.ErrTrialFinished):
		writeError(w, http.StatusConflict, err.Error())
	case preparing:
		writeError(w, http.StatusServiceUnavailable, "task is not ready for experiment")
	default:
		writeError(w, http.StatusInternalServerError, "experiment control failed")
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
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
