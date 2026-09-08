//go:build !linux

package e1awss4

import (
	"context"
	"errors"
	"io"
)

func RunResourceDiagnostic(context.Context, io.Writer, string) error {
	return errors.New("resource diagnostic requires Linux")
}
