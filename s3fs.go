package static

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"time"

	s3 "github.com/hanzos3/go"
	"github.com/hanzos3/go/pkg/credentials"
)

const maxObjectSize = 50 << 20 // 50 MB

// S3Config holds S3 backend configuration.
type S3Config struct {
	// IndexFiles tried for a directory request; defaults to index.html/index.htm.
	IndexFiles []string `json:"indexFiles,omitempty"`
	Endpoint   string   `json:"endpoint,omitempty"`  // S3 endpoint (e.g. s3:9000)
	Bucket     string   `json:"bucket,omitempty"`    // Bucket name
	Region     string   `json:"region,omitempty"`    // Region (default us-east-1)
	AccessKey  string   `json:"accessKey,omitempty"` // Access key ID
	SecretKey  string   `json:"secretKey,omitempty"` // Secret access key
	Prefix     string   `json:"prefix,omitempty"`    // Key prefix
	UseSSL     bool     `json:"useSSL,omitempty"`    // Use HTTPS
}

// S3FS implements http.FileSystem backed by S3-compatible s3.
type S3FS struct {
	client *s3.Client
	bucket string
	prefix string
	// index names tried for a DIRECTORY request. S3 has no directories, so the
	// translation has to happen here — see Open.
	index []string
}

// NewS3FS creates an http.FileSystem backed by S3-compatible s3.
func NewS3FS(_ context.Context, cfg S3Config) (*S3FS, error) {
	endpoint := cfg.Endpoint
	endpoint = strings.TrimPrefix(endpoint, "https://")
	endpoint = strings.TrimPrefix(endpoint, "http://")

	client, err := s3.New(endpoint, &s3.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, err
	}

	index := cfg.IndexFiles
	if len(index) == 0 {
		index = []string{"index.html", "index.htm"}
	}

	return &S3FS{
		client: client,
		bucket: cfg.Bucket,
		prefix: strings.TrimSuffix(cfg.Prefix, "/"),
		index:  index,
	}, nil
}

// Open implements http.FileSystem.
//
// A DIRECTORY REQUEST IS TRANSLATED, NOT REFUSED. On a filesystem `Open("/app/")`
// yields a directory and the handler then looks inside it for an index; S3 has no
// directories, so the same intent has to be expressed by asking for the index key
// itself. Refusing outright — which is what this did — made every static SITE 404
// at its own root and at every sub-route, while every ASSET served perfectly.
// That is exactly why it went unnoticed for so long: the CDN only ever asks for
// files, and a file is the one case that worked.
func (s *S3FS) Open(name string) (http.File, error) {
	key := strings.TrimPrefix(name, "/")
	if s.prefix != "" {
		key = s.prefix + "/" + key
	}
	if key == "" || strings.HasSuffix(key, "/") {
		for _, idx := range s.index {
			if f, err := s.object(key + idx); err == nil {
				return f, nil
			}
		}
		return nil, &os.PathError{Op: "open", Path: name, Err: os.ErrNotExist}
	}

	if f, err := s.object(key); err == nil {
		return f, nil
	}

	// A BARE DIRECTORY PATH IS STILL A DIRECTORY. `/auth/callback` names the same
	// place as `/auth/callback/`, and every other static host resolves it — which
	// is why an OAuth redirect_uri is written without the slash, and why one
	// written that way 404'd here while the slashed form served.
	//
	// Serving the index directly rather than redirecting keeps the request a
	// single round trip and cannot break assets: a static export addresses its
	// own by absolute path.
	for _, idx := range s.index {
		if f, err := s.object(key + "/" + idx); err == nil {
			return f, nil
		}
	}
	return nil, &os.PathError{Op: "open", Path: name, Err: os.ErrNotExist}
}

// object fetches one key, or reports it absent.
func (s *S3FS) object(key string) (http.File, error) {
	name := "/" + key

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	obj, err := s.client.GetObject(ctx, s.bucket, key, s3.GetObjectOptions{})
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: name, Err: os.ErrNotExist}
	}

	info, err := obj.Stat()
	if err != nil {
		obj.Close()
		return nil, &os.PathError{Op: "open", Path: name, Err: os.ErrNotExist}
	}

	if info.Size > maxObjectSize {
		obj.Close()
		return nil, &os.PathError{Op: "open", Path: name, Err: fmt.Errorf("object too large: %d bytes", info.Size)}
	}

	data, err := io.ReadAll(io.LimitReader(obj, maxObjectSize+1))
	obj.Close()
	if err != nil {
		return nil, &os.PathError{Op: "read", Path: name, Err: err}
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
func (i *s3FileInfo) Sys() any           { return nil }
