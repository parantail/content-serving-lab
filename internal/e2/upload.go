package e2

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Uploader runs only between batches. Each immutable artifact is uploaded once,
// with conditional creation so a reused run prefix cannot silently overwrite raw.
type Uploader struct {
	client               *s3.Client
	bucket, prefix, root string
	sent                 map[string]bool
}

func NewUploader(ctx context.Context, bucket, prefix, root string) (*Uploader, error) {
	if bucket == "" {
		return nil, nil
	}
	if !SafeName(prefix) {
		return nil, fmt.Errorf("invalid result prefix")
	}
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &Uploader{s3.NewFromConfig(cfg), bucket, prefix, root, map[string]bool{}}, nil
}
func (u *Uploader) Flush() error {
	if u == nil {
		return nil
	}
	return filepath.WalkDir(u.root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(u.root, path)
		if err != nil {
			return err
		}
		key := u.prefix + "/" + filepath.ToSlash(relative)
		if u.sent[key] {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err = u.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(u.bucket), Key: aws.String(key), Body: file, IfNoneMatch: aws.String("*")})
		if err != nil {
			return fmt.Errorf("result upload failed: %w", err)
		}
		u.sent[key] = true
		return nil
	})
}
