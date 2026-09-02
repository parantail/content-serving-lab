package media

import (
	"context"
	"errors"
)

var ErrOriginalNotFound = errors.New("original not found")

type Transformer interface {
	Transform(ctx context.Context, original []byte, spec TransformSpec) ([]byte, error)
	Version() string
}

type OriginalStore interface {
	Read(ctx context.Context, sourceHash string) ([]byte, error)
}

type DerivativeStore interface {
	Get(ctx context.Context, derivativeKey string) (data []byte, found bool, err error)
	PutIfAbsent(ctx context.Context, derivativeKey string, data []byte) (created bool, err error)
}

type Work func(context.Context) ([]byte, error)

type Coordinator interface {
	Do(requestCtx context.Context, derivativeKey string, work Work) (data []byte, coalesced bool, err error)
	Mode() string
}
