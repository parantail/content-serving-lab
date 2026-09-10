package e2magick

import (
	"fmt"
	"github.com/parantail/content-serving-lab/internal/e2"
	"gopkg.in/gographics/imagick.v3/imagick"
	"strings"
	"sync"
)

var once sync.Once

type Transformer struct{}

func New() *Transformer {
	once.Do(func() {
		imagick.Initialize()
		for r, n := range map[imagick.ResourceType]uint64{imagick.RESOURCE_THREAD: 1, imagick.RESOURCE_MEMORY: 1536 << 20, imagick.RESOURCE_MAP: 0, imagick.RESOURCE_DISK: 0, imagick.RESOURCE_AREA: e2.MaxPixels} {
			if !imagick.SetResourceLimit(r, n) {
				panic("cannot set native resource policy")
			}
		}
		v, _ := imagick.GetVersion()
		if !strings.Contains(v, "Q16") || strings.Contains(v, "HDRI") {
			panic("requires ImageMagick Q16 non-HDRI")
		}
	})
	return &Transformer{}
}
func (*Transformer) Version() string { v, _ := imagick.GetVersion(); return "imagick v3.7.3; " + v }
func (*Transformer) Transform(b []byte, s e2.Spec) ([]byte, error) {
	if e := s.Validate(); e != nil {
		return nil, e
	}
	if e := e2.InputBytes(b); e != nil {
		return nil, e
	}
	mw := imagick.NewMagickWand()
	defer mw.Destroy()
	if e := mw.PingImageBlob(b); e != nil {
		return nil, e
	}
	w, h := int(mw.GetImageWidth()), int(mw.GetImageHeight())
	o := int(mw.GetImageOrientation())
	if e := e2.Dimensions(w, h); e != nil {
		return nil, e
	}
	if mw.GetNumberImages() != 1 {
		return nil, fmt.Errorf("multiple pages")
	}
	mw.Clear()
	if o >= 5 && o <= 8 {
		w, h = h, w
	}
	rw, rh, x, y, ow, oh := e2.Geometry(w, h, s.Operation)
	// jpeg:size requests decoder shrink while retaining room for the final filter.
	options := map[string]string{"jpeg:size": fmt.Sprintf("%dx%d", rw*2, rh*2), "jpeg:sampling-factor": "1x1", "webp:method": "4", "webp:thread-level": "0", "webp:alpha-quality": "100", "webp:lossless": "false", "heic:speed": "5", "heic:chroma": "420", "png:compression-level": "6", "png:compression-filter": "0", "png:exclude-chunk": "date,time"}
	if s.Quality >= 90 {
		options["heic:chroma"] = "444"
	}
	for k, v := range options {
		if e := mw.SetOption(k, v); e != nil {
			return nil, e
		}
	}
	if e := mw.ReadImageBlob(b); e != nil {
		return nil, e
	}
	if e := mw.AutoOrientImage(); e != nil {
		return nil, e
	}
	if e := mw.TransformImageColorspace(imagick.COLORSPACE_SRGB); e != nil {
		return nil, e
	}
	if e := mw.ResizeImage(uint(rw), uint(rh), imagick.FILTER_LANCZOS); e != nil {
		return nil, e
	}
	if s.Operation == "cover" {
		if e := mw.CropImage(uint(ow), uint(oh), x, y); e != nil {
			return nil, e
		}
		if e := mw.ResetImagePage("0x0+0+0"); e != nil {
			return nil, e
		}
	}
	if s.Format == "jpeg" {
		white := imagick.NewPixelWand()
		defer white.Destroy()
		white.SetColor("white")
		if e := mw.SetImageBackgroundColor(white); e != nil {
			return nil, e
		}
		if e := mw.SetImageAlphaChannel(imagick.ALPHA_CHANNEL_REMOVE); e != nil {
			return nil, e
		}
	}
	if e := mw.SetImageDepth(8); e != nil {
		return nil, e
	}
	if e := mw.SetImageFormat(s.Format); e != nil {
		return nil, e
	}
	if e := mw.SetImageCompressionQuality(uint(s.Quality)); e != nil {
		return nil, e
	}
	// HEIC/AVIF reads ImageInfo.quality; setting only Image.quality leaves
	// libheif at its default Q50. Keep both for the different format writers.
	if e := mw.SetCompressionQuality(uint(s.Quality)); e != nil {
		return nil, e
	}
	if s.Format == "png" {
		if e := mw.SetOption("png:color-type", "6"); e != nil {
			return nil, e
		}
	}
	if e := mw.StripImage(); e != nil {
		return nil, e
	}
	return mw.GetImageBlob()
}
