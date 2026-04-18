package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/hanzoai/static"
)

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; font-src 'self' data:; img-src 'self' data: https:; script-src 'self' 'unsafe-inline'")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
		next.ServeHTTP(w, r)
	})
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	port := flag.Int("port", 3000, "listen port")
	root := flag.String("root", "/public", "root directory (local filesystem)")
	spa := flag.Bool("spa", false, "SPA mode (serve index.html for 404s)")

	// S3 backend flags (override local filesystem when S3_BUCKET is set)
	s3Endpoint := flag.String("s3-endpoint", envOr("S3_ENDPOINT", ""), "S3 endpoint URL")
	s3Bucket := flag.String("s3-bucket", envOr("S3_BUCKET", ""), "S3 bucket name")
	s3Region := flag.String("s3-region", envOr("S3_REGION", "us-east-1"), "S3 region")
	s3Prefix := flag.String("s3-prefix", envOr("S3_PREFIX", ""), "S3 key prefix")
	flag.Parse()

	cfg := &static.Config{
		Root:       *root,
		IndexFiles: []string{"index.html"},
		SPAMode:    *spa,
		SPAIndex:   "index.html",
	}

	if *s3Bucket != "" {
		cfg.S3 = &static.S3Config{
			Endpoint:  *s3Endpoint,
			Bucket:    *s3Bucket,
			Region:    *s3Region,
			AccessKey: os.Getenv("AWS_ACCESS_KEY_ID"),
			SecretKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
			Prefix:    *s3Prefix,
		}
	}

	notFound := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", 404)
	})
	handler, err := static.New(context.Background(), notFound, cfg, "static")
	if err != nil {
		log.Fatalf("static: %v", err)
	}

	source := *root
	if *s3Bucket != "" {
		source = fmt.Sprintf("s3://%s/%s", *s3Bucket, *s3Prefix)
	}

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("static: serving %s on %s (spa=%v)", source, addr, *spa)
	log.Fatal(http.ListenAndServe(addr, securityHeaders(handler)))
}
