//go:build linux && e2integration

package e2_test

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/parantail/content-serving-lab/internal/e2"
	"github.com/parantail/content-serving-lab/internal/e2vips"
	"github.com/parantail/content-serving-lab/internal/media"
	"golang.org/x/image/webp"
)

// This is a compatibility connection to E1, not a timing or pixel-equivalence
// claim: E1's image thumbnail and E2's buffer thumbnail use different paths.
func TestE1JPEGToWebPConnection(t *testing.T) {
	input, err := os.ReadFile("../../experiments/e1-cache-stampede/fixtures/landscape-4928x3264.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if e2.Hash(input) != "de206136ed0eeaa42499840f49fa35bd3d57745b8a9580b16f0d6e6393ac7f91" {
		t.Fatal("E1 fixture changed")
	}
	old, err := media.NewVipsTransformerWithConcurrency(1).Transform(context.Background(), input, media.TransformSpec{Width: 640, Height: 640, Fit: media.FitCover, Format: media.FormatWebP, Quality: 80})
	if err != nil {
		t.Fatal(err)
	}
	next, err := e2vips.New().Transform(input, e2.Spec{Operation: "cover", Format: "webp", Quality: 80})
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"e1_image_thumbnail": old, "e2_buffer_thumbnail": next} {
		image, err := webp.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if image.Bounds().Dx() != 640 || image.Bounds().Dy() != 640 {
			t.Fatal("bridge geometry")
		}
		t.Logf("%s bytes=%d sha256=%s dimensions=640x640", name, len(data), e2.Hash(data))
	}
}
