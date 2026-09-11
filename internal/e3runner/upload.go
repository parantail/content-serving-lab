package e3runner

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// ObjectPutter is the S3 subset used to upload raw results.
type ObjectPutter interface {
	PutObject(ctx context.Context, input *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

// UploadDirectory copies every file under directory to bucket/prefix/<relative
// path> with a conditional put so an existing run prefix is never overwritten.
// It returns the number of uploaded objects.
func UploadDirectory(ctx context.Context, client ObjectPutter, bucket, prefix, directory string) (int, error) {
	if client == nil {
		return 0, fmt.Errorf("upload client is required")
	}
	if bucket == "" || prefix == "" {
		return 0, fmt.Errorf("upload bucket and prefix are required")
	}
	prefix = strings.Trim(prefix, "/")
	uploaded := 0
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			return nil
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		key := prefix + "/" + filepath.ToSlash(relative)
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:      aws.String(bucket),
			Key:         aws.String(key),
			Body:        file,
			ContentType: aws.String(contentTypeFor(path)),
			IfNoneMatch: aws.String("*"),
		})
		if err != nil {
			return fmt.Errorf("upload %s: %w", key, err)
		}
		uploaded++
		return nil
	})
	return uploaded, err
}

func contentTypeFor(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return "application/json"
	case ".csv":
		return "text/csv"
	case ".svg":
		return "image/svg+xml"
	case ".jsonl", ".prom", ".txt", ".log":
		return "text/plain"
	default:
		return "application/octet-stream"
	}
}

// NewS3Client loads the default AWS configuration, which on ECS resolves the
// task role, and returns an S3 client.
func NewS3Client(ctx context.Context) (*s3.Client, error) {
	client, err := loadS3Client(ctx)
	if err != nil {
		return nil, err
	}
	return client, nil
}
