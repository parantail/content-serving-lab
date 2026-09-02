package media

import (
	"fmt"
	"io"
	"sync/atomic"
)

type Metrics struct {
	derivativeHits          atomic.Int64
	derivativeMisses        atomic.Int64
	originalSuccess         atomic.Int64
	originalError           atomic.Int64
	transformSuccess        atomic.Int64
	transformError          atomic.Int64
	transformTimeout        atomic.Int64
	transformDurationNanos  atomic.Int64
	transformInflight       atomic.Int64
	transformMaxInflight    atomic.Int64
	requestMissSuccessCount atomic.Int64
	requestMissSuccessNanos atomic.Int64
	requestMissErrorCount   atomic.Int64
	requestMissErrorNanos   atomic.Int64
	requestHitSuccessCount  atomic.Int64
	requestHitSuccessNanos  atomic.Int64
	requestHitErrorCount    atomic.Int64
	requestHitErrorNanos    atomic.Int64
	coalesced               atomic.Int64
	publishCreated          atomic.Int64
	publishExisting         atomic.Int64
	publishError            atomic.Int64
}

type MetricsSnapshot struct {
	DerivativeHits          int64
	DerivativeMisses        int64
	OriginalSuccess         int64
	OriginalError           int64
	TransformSuccess        int64
	TransformError          int64
	TransformTimeout        int64
	TransformDurationNanos  int64
	TransformInflight       int64
	TransformMaxInflight    int64
	RequestMissSuccessCount int64
	RequestMissSuccessNanos int64
	RequestMissErrorCount   int64
	RequestMissErrorNanos   int64
	RequestHitSuccessCount  int64
	RequestHitSuccessNanos  int64
	RequestHitErrorCount    int64
	RequestHitErrorNanos    int64
	Coalesced               int64
	PublishCreated          int64
	PublishExisting         int64
	PublishError            int64
}

func (m *Metrics) Snapshot() MetricsSnapshot {
	return MetricsSnapshot{
		DerivativeHits:          m.derivativeHits.Load(),
		DerivativeMisses:        m.derivativeMisses.Load(),
		OriginalSuccess:         m.originalSuccess.Load(),
		OriginalError:           m.originalError.Load(),
		TransformSuccess:        m.transformSuccess.Load(),
		TransformError:          m.transformError.Load(),
		TransformTimeout:        m.transformTimeout.Load(),
		TransformDurationNanos:  m.transformDurationNanos.Load(),
		TransformInflight:       m.transformInflight.Load(),
		TransformMaxInflight:    m.transformMaxInflight.Load(),
		RequestMissSuccessCount: m.requestMissSuccessCount.Load(),
		RequestMissSuccessNanos: m.requestMissSuccessNanos.Load(),
		RequestMissErrorCount:   m.requestMissErrorCount.Load(),
		RequestMissErrorNanos:   m.requestMissErrorNanos.Load(),
		RequestHitSuccessCount:  m.requestHitSuccessCount.Load(),
		RequestHitSuccessNanos:  m.requestHitSuccessNanos.Load(),
		RequestHitErrorCount:    m.requestHitErrorCount.Load(),
		RequestHitErrorNanos:    m.requestHitErrorNanos.Load(),
		Coalesced:               m.coalesced.Load(),
		PublishCreated:          m.publishCreated.Load(),
		PublishExisting:         m.publishExisting.Load(),
		PublishError:            m.publishError.Load(),
	}
}

func (m *Metrics) WritePrometheus(w io.Writer) error {
	s := m.Snapshot()
	values := []struct {
		name   string
		labels string
		value  int64
	}{
		{"media_derivative_requests_total", `result="hit"`, s.DerivativeHits},
		{"media_derivative_requests_total", `result="miss"`, s.DerivativeMisses},
		{"media_original_reads_total", `result="success"`, s.OriginalSuccess},
		{"media_original_reads_total", `result="error"`, s.OriginalError},
		{"media_transform_attempts_total", `result="success"`, s.TransformSuccess},
		{"media_transform_attempts_total", `result="error"`, s.TransformError},
		{"media_transform_attempts_total", `result="timeout"`, s.TransformTimeout},
		{"media_transform_inflight", "", s.TransformInflight},
		{"media_transform_max_inflight", "", s.TransformMaxInflight},
		{"media_requests_coalesced_total", "", s.Coalesced},
		{"media_derivative_publish_attempts_total", `result="created"`, s.PublishCreated},
		{"media_derivative_publish_attempts_total", `result="existing"`, s.PublishExisting},
		{"media_derivative_publish_attempts_total", `result="error"`, s.PublishError},
	}
	for _, value := range values {
		if value.labels == "" {
			if _, err := fmt.Fprintf(w, "%s %d\n", value.name, value.value); err != nil {
				return err
			}
		} else if _, err := fmt.Fprintf(w, "%s{%s} %d\n", value.name, value.labels, value.value); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "media_transform_duration_seconds_sum %.9f\n", float64(s.TransformDurationNanos)/1e9)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "media_transform_duration_seconds_count %d\n", s.TransformSuccess+s.TransformError+s.TransformTimeout); err != nil {
		return err
	}
	return writeRequestDurationMetrics(w, s)
}

func (m *Metrics) transformStarted() {
	inflight := m.transformInflight.Add(1)
	for {
		current := m.transformMaxInflight.Load()
		if inflight <= current || m.transformMaxInflight.CompareAndSwap(current, inflight) {
			return
		}
	}
}

func (m *Metrics) requestFinished(cache string, success bool, durationNanos int64) {
	if cache == "derivative" {
		if success {
			m.requestHitSuccessCount.Add(1)
			m.requestHitSuccessNanos.Add(durationNanos)
		} else {
			m.requestHitErrorCount.Add(1)
			m.requestHitErrorNanos.Add(durationNanos)
		}
		return
	}
	if success {
		m.requestMissSuccessCount.Add(1)
		m.requestMissSuccessNanos.Add(durationNanos)
	} else {
		m.requestMissErrorCount.Add(1)
		m.requestMissErrorNanos.Add(durationNanos)
	}
}

func writeRequestDurationMetrics(w io.Writer, s MetricsSnapshot) error {
	values := []struct {
		cache, result string
		count, nanos  int64
	}{
		{"miss", "success", s.RequestMissSuccessCount, s.RequestMissSuccessNanos},
		{"miss", "error", s.RequestMissErrorCount, s.RequestMissErrorNanos},
		{"derivative", "success", s.RequestHitSuccessCount, s.RequestHitSuccessNanos},
		{"derivative", "error", s.RequestHitErrorCount, s.RequestHitErrorNanos},
	}
	for _, value := range values {
		labels := fmt.Sprintf(`cache=%q,result=%q`, value.cache, value.result)
		if _, err := fmt.Fprintf(w, "media_request_duration_seconds_sum{%s} %.9f\n", labels, float64(value.nanos)/1e9); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "media_request_duration_seconds_count{%s} %d\n", labels, value.count); err != nil {
			return err
		}
	}
	return nil
}
