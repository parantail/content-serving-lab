package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"testing"

	"github.com/davidbyttow/govips/v2/vips"
)

func TestVipsTransformerProducesDeterministic640SquareWebP(t *testing.T) {
	original, err := os.ReadFile("../../experiments/e1-cache-stampede/fixtures/landscape-4928x3264.jpg")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := ParseTransformSpec("width=640,height=640,fit=cover,quality=80", "webp")
	if err != nil {
		t.Fatal(err)
	}
	transformer := NewVipsTransformer()

	first, err := transformer.Transform(context.Background(), original, spec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := transformer.Transform(context.Background(), original, spec)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		firstHash := sha256.Sum256(first)
		secondHash := sha256.Sum256(second)
		t.Fatalf("output hashes differ: %x != %x", firstHash, secondHash)
	}

	output, err := vips.NewImageFromBuffer(first)
	if err != nil {
		t.Fatalf("open output WebP: %v", err)
	}
	defer output.Close()
	if output.Width() != 640 || output.Height() != 640 {
		t.Fatalf("output dimensions = %dx%d, want 640x640", output.Width(), output.Height())
	}
	if imageType := vips.DetermineImageType(first); imageType != vips.ImageTypeWEBP {
		t.Fatalf("output type = %v, want WebP", imageType)
	}
}
