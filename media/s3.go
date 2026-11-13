package media

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type objectStore struct {
	client *minio.Client
	bucket string
}

var (
	storeMu sync.RWMutex
	store   *objectStore
)

// InitObjectStore initializes S3/MinIO storage when the environment is configured.
// Local disk remains the fallback for installations without object storage.
func InitObjectStore() error {
	mu := strings.TrimSpace(os.Getenv("S3_ENDPOINT"))
	access := strings.TrimSpace(os.Getenv("S3_ACCESS_KEY"))
	secret := strings.TrimSpace(os.Getenv("S3_SECRET_KEY"))
	bucket := strings.TrimSpace(os.Getenv("S3_BUCKET"))

	if mu == "" || access == "" || secret == "" || bucket == "" {
		return nil
	}

	secure := strings.EqualFold(strings.TrimSpace(os.Getenv("S3_USE_SSL")), "true")
	client, err := minio.New(strings.TrimPrefix(strings.TrimPrefix(mu, "http://"), "https://"), &minio.Options{
		Creds:  credentials.NewStaticV4(access, secret, ""),
		Secure: secure,
	})
	if err != nil {
		return fmt.Errorf("initialize object storage: %w", err)
	}

	ctx := context.Background()
	exists, err := client.BucketExists(ctx, bucket)
	if err != nil {
		return fmt.Errorf("check object bucket: %w", err)
	}
	if !exists {
		if err := client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
			return fmt.Errorf("create object bucket: %w", err)
		}
	}

	storeMu.Lock()
	store = &objectStore{client: client, bucket: bucket}
	storeMu.Unlock()

	return nil
}

func objectStoreEnabled() bool {
	storeMu.RLock()
	defer storeMu.RUnlock()

	return store != nil
}

func putObject(name string, data []byte) error {
	storeMu.RLock()
	current := store
	storeMu.RUnlock()

	if current == nil {
		return nil
	}

	_, err := current.client.PutObject(
		context.Background(),
		current.bucket,
		name,
		bytes.NewReader(data),
		int64(len(data)),
		minio.PutObjectOptions{ContentType: contentTypeForName(name)},
	)
	if err != nil {
		return fmt.Errorf("upload object: %w", err)
	}

	return nil
}

func openObject(name string) (io.ReadCloser, int64, string, bool) {
	storeMu.RLock()
	current := store
	storeMu.RUnlock()

	if current == nil {
		return nil, 0, "", false
	}

	object, err := current.client.GetObject(context.Background(), current.bucket, name, minio.GetObjectOptions{})
	if err != nil {
		return nil, 0, "", false
	}
	defer func() {
		_ = object.Close()
	}()

	data, err := io.ReadAll(io.LimitReader(object, 30<<20))
	if err != nil {
		return nil, 0, "", false
	}

	return io.NopCloser(bytes.NewReader(data)), int64(len(data)), contentTypeForName(name), true
}

func contentTypeForName(name string) string {
	switch strings.ToLower(name[strings.LastIndex(name, ".")+1:]) {
	case "jpg", "jpeg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	case "svg":
		return "image/svg+xml"
	case "mp4":
		return "video/mp4"
	case "webm":
		return "video/webm"
	case "mp3":
		return "audio/mpeg"
	case "ogg":
		return "audio/ogg"
	case "wav":
		return "audio/wav"
	case "pdf":
		return "application/pdf"
	case "zip":
		return "application/zip"
	default:
		return "application/octet-stream"
	}
}

func openObjectRange(name string, start, end int64) (io.ReadCloser, bool) {
	storeMu.RLock()
	current := store
	storeMu.RUnlock()

	if current == nil {
		return nil, false
	}

	options := minio.GetObjectOptions{}
	options.SetRange(start, end)
	object, err := current.client.GetObject(context.Background(), current.bucket, name, options)
	if err != nil {
		return nil, false
	}

	return object, true
}
