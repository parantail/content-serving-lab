package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

const (
	S3OriginalObjectPrefix   = "originals/"
	S3DerivativeObjectPrefix = "derivatives/"

	DefaultS3ConflictRetries = 2
)

var ErrS3ConditionalWriteConflict = errors.New("S3 conditional write conflict")

type S3ObjectClient interface {
	GetObject(ctx context.Context, input *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(ctx context.Context, input *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

var (
	_ OriginalStore   = (*S3OriginalStore)(nil)
	_ DerivativeStore = (*S3DerivativeStore)(nil)
)

type S3OriginalStore struct {
	client  S3ObjectClient
	bucket  string
	metrics *S3Metrics
}

func NewS3OriginalStore(client S3ObjectClient, bucket string, metrics ...*S3Metrics) (*S3OriginalStore, error) {
	if client == nil {
		return nil, errors.New("S3 original store client is required")
	}
	if bucket == "" {
		return nil, errors.New("S3 original store bucket is required")
	}
	if len(metrics) > 1 {
		return nil, errors.New("S3 original store accepts at most one metrics recorder")
	}
	var recorder *S3Metrics
	if len(metrics) == 1 {
		recorder = metrics[0]
	}
	return &S3OriginalStore{client: client, bucket: bucket, metrics: recorder}, nil
}

func (s *S3OriginalStore) Read(ctx context.Context, sourceHash string) ([]byte, error) {
	if err := validateSHA256Key(sourceHash, "source"); err != nil {
		return nil, err
	}
	data, found, err := getS3Object(ctx, s.client, s.bucket, S3OriginalObjectPrefix+sourceHash)
	if err != nil {
		s.metrics.recordOriginalGet("error", 0)
		return nil, fmt.Errorf("get original object: %w", err)
	}
	if !found {
		s.metrics.recordOriginalGet("error", 0)
		return nil, ErrOriginalNotFound
	}
	s.metrics.recordOriginalGet("success", len(data))
	return data, nil
}

type S3DerivativeStore struct {
	client          S3ObjectClient
	bucket          string
	conflictRetries int
	metrics         *S3Metrics
}

func NewS3DerivativeStore(client S3ObjectClient, bucket string, metrics ...*S3Metrics) (*S3DerivativeStore, error) {
	if client == nil {
		return nil, errors.New("S3 derivative store client is required")
	}
	if bucket == "" {
		return nil, errors.New("S3 derivative store bucket is required")
	}
	if len(metrics) > 1 {
		return nil, errors.New("S3 derivative store accepts at most one metrics recorder")
	}
	var recorder *S3Metrics
	if len(metrics) == 1 {
		recorder = metrics[0]
	}
	return &S3DerivativeStore{
		client:          client,
		bucket:          bucket,
		conflictRetries: DefaultS3ConflictRetries,
		metrics:         recorder,
	}, nil
}

func (s *S3DerivativeStore) Get(ctx context.Context, derivativeKey string) ([]byte, bool, error) {
	objectKey, err := s3DerivativeObjectKey(derivativeKey)
	if err != nil {
		return nil, false, err
	}
	data, found, err := getS3Object(ctx, s.client, s.bucket, objectKey)
	if err != nil {
		s.metrics.recordDerivativeGet("error", 0)
		return nil, false, fmt.Errorf("get derivative object: %w", err)
	}
	if !found {
		s.metrics.recordDerivativeGet("miss", 0)
		return nil, false, nil
	}
	s.metrics.recordDerivativeGet("hit", len(data))
	return data, found, nil
}

func (s *S3DerivativeStore) PutIfAbsent(ctx context.Context, derivativeKey string, data []byte) (bool, error) {
	objectKey, err := s3DerivativeObjectKey(derivativeKey)
	if err != nil {
		return false, err
	}

	for attempt := 0; attempt <= s.conflictRetries; attempt++ {
		_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:      aws.String(s.bucket),
			Key:         aws.String(objectKey),
			Body:        bytes.NewReader(data),
			ContentType: aws.String("image/webp"),
			IfNoneMatch: aws.String("*"),
		})
		if err == nil {
			s.metrics.recordPublish("created", len(data))
			return true, nil
		}
		if isS3Error(err, http.StatusPreconditionFailed, "PreconditionFailed") {
			s.metrics.recordPublish("existing", len(data))
			return false, nil
		}
		if !isS3Error(err, http.StatusConflict, "ConditionalRequestConflict") {
			s.metrics.recordPublish("error", len(data))
			return false, fmt.Errorf("put derivative object: %w", err)
		}
		s.metrics.recordPublish("conflict", len(data))
		if contextErr := ctx.Err(); contextErr != nil {
			return false, contextErr
		}
		if attempt == s.conflictRetries {
			return false, fmt.Errorf("%w after %d attempts: %v", ErrS3ConditionalWriteConflict, attempt+1, err)
		}
	}

	panic("unreachable")
}

func (m *S3Metrics) recordDerivativeGet(result string, bytes int) {
	if m == nil {
		return
	}
	switch result {
	case "hit":
		m.derivativeGetHit.Add(1)
		m.derivativeGetBytes.Add(int64(bytes))
	case "miss":
		m.derivativeGetMiss.Add(1)
	case "error":
		m.derivativeGetError.Add(1)
	}
}

func (m *S3Metrics) recordOriginalGet(result string, bytes int) {
	if m == nil {
		return
	}
	if result == "success" {
		m.originalGetSuccess.Add(1)
		m.originalGetBytes.Add(int64(bytes))
		return
	}
	m.originalGetError.Add(1)
}

func (m *S3Metrics) recordPublish(result string, bytes int) {
	if m == nil {
		return
	}
	m.publishAttemptBytes.Add(int64(bytes))
	switch result {
	case "created":
		m.publishCreated.Add(1)
	case "existing":
		m.publishExisting.Add(1)
	case "conflict":
		m.publishConflict.Add(1)
	case "error":
		m.publishError.Add(1)
	}
}

func s3DerivativeObjectKey(derivativeKey string) (string, error) {
	if err := validateSHA256Key(derivativeKey, "derivative"); err != nil {
		return "", err
	}
	return S3DerivativeObjectPrefix + derivativeKey + ".webp", nil
}

func getS3Object(ctx context.Context, client S3ObjectClient, bucket, objectKey string) ([]byte, bool, error) {
	output, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		if isS3Error(err, http.StatusNotFound, "NoSuchKey", "NotFound") {
			return nil, false, nil
		}
		return nil, false, err
	}
	if output == nil || output.Body == nil {
		return nil, false, errors.New("S3 GetObject returned an empty body")
	}

	data, readErr := io.ReadAll(output.Body)
	closeErr := output.Body.Close()
	if readErr != nil {
		return nil, false, fmt.Errorf("read S3 object body: %w", readErr)
	}
	if closeErr != nil {
		return nil, false, fmt.Errorf("close S3 object body: %w", closeErr)
	}
	return data, true, nil
}

func isS3Error(err error, statusCode int, errorCodes ...string) bool {
	var responseError interface{ HTTPStatusCode() int }
	if errors.As(err, &responseError) && responseError.HTTPStatusCode() == statusCode {
		return true
	}

	var apiError smithy.APIError
	if !errors.As(err, &apiError) {
		return false
	}
	for _, code := range errorCodes {
		if apiError.ErrorCode() == code {
			return true
		}
	}
	return false
}
