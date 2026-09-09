package e2vips

import (
	"fmt"
	"github.com/davidbyttow/govips/v2/vips"
	"github.com/parantail/content-serving-lab/internal/e2"
	"sync"
)

var once sync.Once

type Transformer struct{}

func New() *Transformer {
	once.Do(func() {
		vips.LoggingSettings(nil, vips.LogLevelWarning)
		vips.Startup(&vips.Config{ConcurrencyLevel: 1, MaxCacheFiles: 0, MaxCacheMem: 0, MaxCacheSize: 0})
	})
	return &Transformer{}
}
func (*Transformer) Version() string { return "govips v2.16.0; libvips " + vips.Version }
func (*Transformer) Transform(b []byte, s e2.Spec) ([]byte, error) {
	if e := s.Validate(); e != nil {
		return nil, e
	}
	if e := e2.InputBytes(b); e != nil {
		return nil, e
	}
	header, e := vips.NewImageFromBuffer(b)
	if e != nil {
		return nil, e
	}
	w, h := header.Width(), header.Height()
	o := header.Orientation()
	pages := header.Pages()
	header.Close()
	if e = e2.Dimensions(w, h); e != nil {
		return nil, e
	}
	if pages > 1 {
		return nil, fmt.Errorf("multiple pages")
	}
	if o >= 5 && o <= 8 {
		w, h = h, w
	}
	rw, rh, x, y, ow, oh := e2.Geometry(w, h, s.Operation)
	// Force the common rounded intermediate dimensions; thumbnail_buffer still
	// selects loader shrink-on-load. Crop coordinates are shared with MagickWand.
	image, e := vips.LoadThumbnailFromBuffer(b, rw, rh, vips.InterestingNone, vips.SizeForce, nil)
	if e != nil {
		return nil, e
	}
	defer image.Close()
	if s.Operation == "cover" {
		if e = image.ExtractArea(x, y, ow, oh); e != nil {
			return nil, e
		}
	}
	if s.Format == "jpeg" && image.HasAlpha() {
		if e = image.Flatten(&vips.Color{R: 255, G: 255, B: 255}); e != nil {
			return nil, e
		}
	}
	var out []byte
	switch s.Format {
	case "jpeg":
		p := vips.NewJpegExportParams()
		p.Quality = s.Quality
		p.Interlace = false
		p.StripMetadata = true
		p.SubsampleMode = vips.VipsForeignSubsampleOff
		out, _, e = image.ExportJpeg(p)
	case "png":
		p := vips.NewPngExportParams()
		p.Compression = 6
		p.Bitdepth = 8
		p.StripMetadata = true
		out, _, e = image.ExportPng(p)
	case "webp":
		p := vips.NewWebpExportParams()
		p.Quality = s.Quality
		p.ReductionEffort = 4
		p.StripMetadata = true
		out, _, e = image.ExportWebp(p)
	case "avif":
		p := vips.NewAvifExportParams()
		p.Quality = s.Quality
		p.Effort = 4
		p.Bitdepth = 8
		p.StripMetadata = true
		out, _, e = image.ExportAvif(p)
	}
	return out, e
}
