package media

import (
	"context"
	"fmt"
	"sync"

	"github.com/davidbyttow/govips/v2/vips"
)

const GovipsVersion = "v2.16.0"

const DefaultTransformConcurrency = 4

var startVipsOnce sync.Once

func StartVips() {
	startVipsOnce.Do(func() {
		vips.LoggingSettings(nil, vips.LogLevelWarning)
		vips.Startup(&vips.Config{
			ConcurrencyLevel: 1,
			MaxCacheFiles:    0,
			MaxCacheMem:      0,
			MaxCacheSize:     0,
			ReportLeaks:      false,
			CacheTrace:       false,
			CollectStats:     true,
		})
	})
}

type VipsTransformer struct {
	slots chan struct{}
}

func NewVipsTransformer() *VipsTransformer {
	return NewVipsTransformerWithConcurrency(DefaultTransformConcurrency)
}

func NewVipsTransformerWithConcurrency(maxConcurrent int) *VipsTransformer {
	StartVips()
	if maxConcurrent < 1 {
		panic("media: transform concurrency must be positive")
	}
	return &VipsTransformer{slots: make(chan struct{}, maxConcurrent)}
}

func (t *VipsTransformer) Transform(ctx context.Context, original []byte, spec TransformSpec) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if spec.Fit != FitCover || spec.Format != FormatWebP {
		return nil, fmt.Errorf("%w: vips transformer only supports cover WebP", ErrInvalidSpec)
	}
	select {
	case t.slots <- struct{}{}:
		defer func() { <-t.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	image, err := vips.NewImageFromBuffer(original)
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}
	defer image.Close()

	if err := image.Thumbnail(spec.Width, spec.Height, vips.InterestingCentre); err != nil {
		return nil, fmt.Errorf("resize image: %w", err)
	}
	params := vips.NewWebpExportParams()
	params.Quality = spec.Quality
	params.StripMetadata = true
	data, _, err := image.ExportWebp(params)
	if err != nil {
		return nil, fmt.Errorf("encode WebP: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

func (*VipsTransformer) Version() string {
	return VipsTransformerVersion()
}

func VipsTransformerVersion() string {
	return "govips " + GovipsVersion + "; libvips " + vips.Version
}
