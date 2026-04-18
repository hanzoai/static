package static

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"time"

	storage "github.com/hanzoai/storage-go"
	"github.com/hanzoai/storage-go/pkg/credentials"
)

// S3Config holds S3 backend configuration.
type S3Config struct {
	Endpoint  string `json:"endpoint,omitempty"`  // S3 endpoint (e.g. s3:9000)
	Bucket    string `json:"bucket,omitempty"`    // Bucket name
	Region    string `json:"region,omitempty"`    // Region (default us-east-1)
	AccessKey string `json:"accessKey,omitempty"` // Access key ID
	SecretKey string `json:"secretKey,omitempty"` // Secret access key
	Prefix    string `json:"prefix,omitempty"`    // Key prefix
	UseSSL    bool   `json:"useSSL,omitempty"`    // Use HTTPS
}

// S3FS implements http.FileSystem backed by S3-compatible storage.
type S3FS struct {
	client *storage.Client
	bucket string
	prefix string
}

// NewS3FS creates an http.FileSystem backed by S3-compatible storage.
func NewS3FS(_ context.Context, cfg S3Config) (*S3FS, error) {
	endpoint := cfg.Endpoint
	endpoint = strings.TrimPrefix(endpoint, "https://")
	endpoint = strings.TrimPrefix(endpoint, "http://")

	client, err := storage.New(endpoint, &storage.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, err
	}

	return &S3FS{
		client: client,
		bucket: cfg.Bucket,
		prefix: strings.TrimSuffix(cfg.Prefix, "/"),
	}, nil
}

// Open implements http.FileSystem.
func (s *S3FS) Open(name string) (http.File, error) {
	key := strings.TrimPrefix(name, "/")
	if s.prefix != "" {
		key = s.prefix + "/" + key
	}
	if key == "" || strings.HasSuffix(key, "/") {
		return nil, &os.PathError{Op: "open", Path: name, Err: os.ErrNotExist}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	obj, err := s.client.GetObject(ctx, s.bucket, key, storage.GetObjectOptions{})
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: name, Err: os.ErrNotExist}
	}

	info, err := obj.Stat()
	if err != nil {
		obj.Close()
		return nil, &os.PathError{Op: "open", Path: name, Err: os.ErrNotExist}
	}

	data, err := io.ReadAll(obj)
	obj.Close()
	if err != nil {
		return nil, err
	}

	return &s3File{
		Reader: bytes.NewReader(data),
		name:   name,
		size:   info.Size,
		mod:    info.LastModified,
	}, nil
}

type s3File struct {
	*bytes.Reader
	name string
	size int64
	mod  time.Time
}

func (f *s3File) Close() error { return nil }

func (f *s3File) Readdir(int) ([]fs.FileInfo, error) {
	return nil, &os.PathError{Op: "readdir", Path: f.name, Err: os.ErrInvalid}
}

func (f *s3File) Stat() (fs.FileInfo, error) {
	return &s3FileInfo{name: f.name, size: f.size, mod: f.mod}, nil
}

type s3FileInfo struct {
	name string
	size int64
	mod  time.Time
}

func (i *s3FileInfo) Name() string       { return i.name }
func (i *s3FileInfo) Size() int64        { return i.size }
func (i *s3FileInfo) Mode() fs.FileMode  { return 0444 }
func (i *s3FileInfo) ModTime() time.Time { return i.mod }
func (i *s3FileInfo) IsDir() bool        { return false }
func (i *s3FileInfo) Sys() interface{}   { return nil }
