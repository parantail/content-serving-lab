package media

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var ErrInvalidSpec = errors.New("invalid transform specification")

const (
	FitCover   = "cover"
	FormatWebP = "webp"
)

type TransformSpec struct {
	Width   int
	Height  int
	Fit     string
	Format  string
	Quality int
}

func ParseTransformSpec(raw, format string) (TransformSpec, error) {
	values := make(map[string]string)
	for _, part := range strings.Split(raw, ",") {
		name, value, ok := strings.Cut(part, "=")
		if !ok || name == "" || value == "" {
			return TransformSpec{}, fmt.Errorf("%w: expected name=value fields", ErrInvalidSpec)
		}
		name = strings.ToLower(name)
		if _, exists := values[name]; exists {
			return TransformSpec{}, fmt.Errorf("%w: duplicate field %q", ErrInvalidSpec, name)
		}
		values[name] = strings.ToLower(value)
	}

	if len(values) != 4 {
		return TransformSpec{}, fmt.Errorf("%w: width, height, fit and quality are required", ErrInvalidSpec)
	}

	width, err := parseBoundedInt(values, "width", 1, 4096)
	if err != nil {
		return TransformSpec{}, err
	}
	height, err := parseBoundedInt(values, "height", 1, 4096)
	if err != nil {
		return TransformSpec{}, err
	}
	quality, err := parseBoundedInt(values, "quality", 1, 100)
	if err != nil {
		return TransformSpec{}, err
	}
	if values["fit"] != FitCover {
		return TransformSpec{}, fmt.Errorf("%w: unsupported fit %q", ErrInvalidSpec, values["fit"])
	}
	format = strings.ToLower(format)
	if format != FormatWebP {
		return TransformSpec{}, fmt.Errorf("%w: unsupported format %q", ErrInvalidSpec, format)
	}

	return TransformSpec{
		Width:   width,
		Height:  height,
		Fit:     FitCover,
		Format:  FormatWebP,
		Quality: quality,
	}, nil
}

func parseBoundedInt(values map[string]string, name string, minValue, maxValue int) (int, error) {
	raw, ok := values[name]
	if !ok {
		return 0, fmt.Errorf("%w: missing field %q", ErrInvalidSpec, name)
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minValue || value > maxValue {
		return 0, fmt.Errorf("%w: %s must be between %d and %d", ErrInvalidSpec, name, minValue, maxValue)
	}
	return value, nil
}

func (s TransformSpec) Canonical() string {
	return fmt.Sprintf(
		"width=%d,height=%d,fit=%s,quality=%d,format=%s",
		s.Width,
		s.Height,
		s.Fit,
		s.Quality,
		s.Format,
	)
}

func ValidateSourceHash(sourceHash string) error {
	if len(sourceHash) != sha256.Size*2 {
		return fmt.Errorf("%w: source hash must be a SHA-256 hex string", ErrInvalidSpec)
	}
	if _, err := hex.DecodeString(sourceHash); err != nil {
		return fmt.Errorf("%w: source hash must be a SHA-256 hex string", ErrInvalidSpec)
	}
	return nil
}

func DerivativeKey(sourceHash string, spec TransformSpec) (string, error) {
	if err := ValidateSourceHash(sourceHash); err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(strings.ToLower(sourceHash) + "\n" + spec.Canonical()))
	return hex.EncodeToString(digest[:]), nil
}
