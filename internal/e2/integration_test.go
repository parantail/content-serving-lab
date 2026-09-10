//go:build linux && e2integration

package e2_test

import (
	"bytes"
	"github.com/parantail/content-serving-lab/internal/e2"
	"github.com/parantail/content-serving-lab/internal/e2magick"
	"github.com/parantail/content-serving-lab/internal/e2vips"
	_ "golang.org/x/image/webp"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	"os"
	"testing"
)

func TestNativeGeometryAlphaAndLimits(t *testing.T) {
	input := image.NewNRGBA(image.Rect(0, 0, 64, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			input.SetNRGBA(x, y, color.NRGBA{R: 255, A: 128})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, input); err != nil {
		t.Fatal(err)
	}
	for name, engine := range map[string]e2.Transformer{"vips": e2vips.New(), "magick": e2magick.New()} {
		t.Run(name, func(t *testing.T) {
			for _, op := range []string{"resize", "contain", "cover"} {
				for _, format := range []string{"jpeg", "png", "webp", "avif"} {
					t.Run(op+"-"+format, func(t *testing.T) {
						out, err := engine.Transform(encoded.Bytes(), e2.Spec{Operation: op, Format: format, Quality: 80})
						if err != nil {
							t.Fatal(err)
						}
						if len(out) == 0 {
							t.Fatal("empty output")
						}
						if format == "avif" {
							if err = e2.InputBytes(out); err != nil {
								t.Fatal(err)
							}
							return
						}
						got, _, err := image.Decode(bytes.NewReader(out))
						if err != nil {
							t.Fatal(err)
						}
						if got.Bounds().Dx() != 64 || got.Bounds().Dy() != 48 {
							t.Fatal("unexpected upscale/crop", got.Bounds())
						}
						pixel := color.NRGBAModel.Convert(got.At(32, 24)).(color.NRGBA)
						if format == "jpeg" {
							if pixel.A != 255 || pixel.R < 250 || pixel.G < 122 || pixel.G > 132 || pixel.B < 122 || pixel.B > 132 {
								t.Fatal("white alpha flatten", pixel)
							}
						} else if pixel.A < 126 || pixel.A > 130 {
							t.Fatal("alpha lost", pixel)
						}
					})
				}
			}
			for _, filename := range []string{"truncated.jpg", "unsupported.txt", "oversize.png"} {
				raw, err := os.ReadFile("../../experiments/e2-transformer-ab/fixtures/generated/invalid/" + filename)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = engine.Transform(raw, e2.Spec{Operation: "cover", Format: "png", Quality: 80}); err == nil {
					t.Fatal("invalid input accepted", filename)
				}
			}
		})
	}
}

// Q50 and Q80 share AVIF 4:2:0, so a chroma switch cannot hide a no-op setter.
func TestNativeAVIFQualityResponds(t *testing.T) {
	input := image.NewNRGBA(image.Rect(0, 0, 64, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			input.SetNRGBA(x, y, color.NRGBA{R: uint8(x*17 + y*3), G: uint8(x*7 + y*19), B: uint8(x*31 + y*11), A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, input); err != nil {
		t.Fatal(err)
	}
	for name, engine := range map[string]e2.Transformer{"vips": e2vips.New(), "magick": e2magick.New()} {
		t.Run(name, func(t *testing.T) {
			low, err := engine.Transform(encoded.Bytes(), e2.Spec{Operation: "cover", Format: "avif", Quality: 50})
			if err != nil {
				t.Fatal(err)
			}
			high, err := engine.Transform(encoded.Bytes(), e2.Spec{Operation: "cover", Format: "avif", Quality: 80})
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(low, high) {
				t.Fatal("AVIF quality setter has no effect")
			}
		})
	}
}
