package media

import "sync/atomic"

type S3Metrics struct {
	derivativeGetHit    atomic.Int64
	derivativeGetMiss   atomic.Int64
	derivativeGetError  atomic.Int64
	derivativeGetBytes  atomic.Int64
	originalGetSuccess  atomic.Int64
	originalGetError    atomic.Int64
	originalGetBytes    atomic.Int64
	publishCreated      atomic.Int64
	publishExisting     atomic.Int64
	publishConflict     atomic.Int64
	publishError        atomic.Int64
	publishAttemptBytes atomic.Int64
}

type S3MetricsSnapshot struct {
	DerivativeGetHit    int64 `json:"derivative_get_hit"`
	DerivativeGetMiss   int64 `json:"derivative_get_miss"`
	DerivativeGetError  int64 `json:"derivative_get_error"`
	DerivativeGetBytes  int64 `json:"derivative_get_bytes"`
	OriginalGetSuccess  int64 `json:"original_get_success"`
	OriginalGetError    int64 `json:"original_get_error"`
	OriginalGetBytes    int64 `json:"original_get_bytes"`
	PublishCreated      int64 `json:"publish_created"`
	PublishExisting     int64 `json:"publish_existing"`
	PublishConflict     int64 `json:"publish_conflict"`
	PublishError        int64 `json:"publish_error"`
	PublishAttemptBytes int64 `json:"publish_attempt_bytes"`
}

func (m *S3Metrics) Snapshot() S3MetricsSnapshot {
	if m == nil {
		return S3MetricsSnapshot{}
	}
	return S3MetricsSnapshot{
		DerivativeGetHit:    m.derivativeGetHit.Load(),
		DerivativeGetMiss:   m.derivativeGetMiss.Load(),
		DerivativeGetError:  m.derivativeGetError.Load(),
		DerivativeGetBytes:  m.derivativeGetBytes.Load(),
		OriginalGetSuccess:  m.originalGetSuccess.Load(),
		OriginalGetError:    m.originalGetError.Load(),
		OriginalGetBytes:    m.originalGetBytes.Load(),
		PublishCreated:      m.publishCreated.Load(),
		PublishExisting:     m.publishExisting.Load(),
		PublishConflict:     m.publishConflict.Load(),
		PublishError:        m.publishError.Load(),
		PublishAttemptBytes: m.publishAttemptBytes.Load(),
	}
}

func (s S3MetricsSnapshot) Subtract(baseline S3MetricsSnapshot) S3MetricsSnapshot {
	return S3MetricsSnapshot{
		DerivativeGetHit:    s.DerivativeGetHit - baseline.DerivativeGetHit,
		DerivativeGetMiss:   s.DerivativeGetMiss - baseline.DerivativeGetMiss,
		DerivativeGetError:  s.DerivativeGetError - baseline.DerivativeGetError,
		DerivativeGetBytes:  s.DerivativeGetBytes - baseline.DerivativeGetBytes,
		OriginalGetSuccess:  s.OriginalGetSuccess - baseline.OriginalGetSuccess,
		OriginalGetError:    s.OriginalGetError - baseline.OriginalGetError,
		OriginalGetBytes:    s.OriginalGetBytes - baseline.OriginalGetBytes,
		PublishCreated:      s.PublishCreated - baseline.PublishCreated,
		PublishExisting:     s.PublishExisting - baseline.PublishExisting,
		PublishConflict:     s.PublishConflict - baseline.PublishConflict,
		PublishError:        s.PublishError - baseline.PublishError,
		PublishAttemptBytes: s.PublishAttemptBytes - baseline.PublishAttemptBytes,
	}
}
