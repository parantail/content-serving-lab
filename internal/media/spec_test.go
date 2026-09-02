package media

import (
	"errors"
	"testing"
)

const testSourceHash = "de206136ed0eeaa42499840f49fa35bd3d57745b8a9580b16f0d6e6393ac7f91"

func TestEquivalentSpecsHaveTheSameCanonicalValueAndKey(t *testing.T) {
	t.Parallel()

	first, err := ParseTransformSpec("width=640,height=640,fit=cover,quality=80", "webp")
	if err != nil {
		t.Fatal(err)
	}
	second, err := ParseTransformSpec("quality=80,fit=COVER,height=640,width=640", "WEBP")
	if err != nil {
		t.Fatal(err)
	}
	if first.Canonical() != second.Canonical() {
		t.Fatalf("canonical values differ: %q != %q", first.Canonical(), second.Canonical())
	}

	firstKey, err := DerivativeKey(testSourceHash, first)
	if err != nil {
		t.Fatal(err)
	}
	secondKey, err := DerivativeKey(testSourceHash, second)
	if err != nil {
		t.Fatal(err)
	}
	if firstKey != secondKey {
		t.Fatalf("keys differ: %q != %q", firstKey, secondKey)
	}
}

func TestDifferentSpecsHaveDifferentKeys(t *testing.T) {
	t.Parallel()

	first, _ := ParseTransformSpec("width=640,height=640,fit=cover,quality=80", "webp")
	second, _ := ParseTransformSpec("width=1280,height=1280,fit=cover,quality=80", "webp")
	firstKey, _ := DerivativeKey(testSourceHash, first)
	secondKey, _ := DerivativeKey(testSourceHash, second)
	if firstKey == secondKey {
		t.Fatalf("different specs produced key %q", firstKey)
	}
}

func TestInvalidSpecsAreRejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		raw    string
		format string
	}{
		{name: "missing field", raw: "width=640,height=640,fit=cover", format: "webp"},
		{name: "duplicate field", raw: "width=640,width=640,height=640,fit=cover,quality=80", format: "webp"},
		{name: "bad width", raw: "width=0,height=640,fit=cover,quality=80", format: "webp"},
		{name: "bad quality", raw: "width=640,height=640,fit=cover,quality=101", format: "webp"},
		{name: "bad fit", raw: "width=640,height=640,fit=contain,quality=80", format: "webp"},
		{name: "bad format", raw: "width=640,height=640,fit=cover,quality=80", format: "jpeg"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseTransformSpec(tt.raw, tt.format)
			if !errors.Is(err, ErrInvalidSpec) {
				t.Fatalf("error = %v, want ErrInvalidSpec", err)
			}
		})
	}
}
