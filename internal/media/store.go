package media

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type FileOriginalStore struct {
	files map[string]string
}

func NewFileOriginalStore(files map[string]string) *FileOriginalStore {
	copyOfFiles := make(map[string]string, len(files))
	for sourceHash, path := range files {
		copyOfFiles[sourceHash] = path
	}
	return &FileOriginalStore{files: copyOfFiles}
}

func (s *FileOriginalStore) Read(ctx context.Context, sourceHash string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, ok := s.files[sourceHash]
	if !ok {
		return nil, ErrOriginalNotFound
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrOriginalNotFound
	}
	return data, err
}

type LocalDerivativeStore struct {
	directory string
}

func NewLocalDerivativeStore(directory string) (*LocalDerivativeStore, error) {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, fmt.Errorf("create derivative directory: %w", err)
	}
	return &LocalDerivativeStore{directory: directory}, nil
}

func (s *LocalDerivativeStore) Get(ctx context.Context, derivativeKey string) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	path, err := s.path(derivativeKey)
	if err != nil {
		return nil, false, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func (s *LocalDerivativeStore) PutIfAbsent(ctx context.Context, derivativeKey string, data []byte) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	finalPath, err := s.path(derivativeKey)
	if err != nil {
		return false, err
	}

	temporary, err := os.CreateTemp(s.directory, ".derivative-*.tmp")
	if err != nil {
		return false, fmt.Errorf("create temporary derivative: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return false, fmt.Errorf("write temporary derivative: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return false, fmt.Errorf("sync temporary derivative: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return false, fmt.Errorf("close temporary derivative: %w", err)
	}

	if err := os.Link(temporaryPath, finalPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, fmt.Errorf("publish derivative: %w", err)
	}
	return true, nil
}

func (s *LocalDerivativeStore) path(derivativeKey string) (string, error) {
	if err := validateSHA256Key(derivativeKey, "derivative"); err != nil {
		return "", err
	}
	return filepath.Join(s.directory, derivativeKey+".webp"), nil
}

func validateSHA256Key(key, kind string) error {
	if len(key) != 64 {
		return fmt.Errorf("invalid %s key %q", kind, key)
	}
	for _, char := range key {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return fmt.Errorf("invalid %s key %q", kind, key)
		}
	}
	return nil
}
