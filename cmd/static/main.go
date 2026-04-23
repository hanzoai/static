package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/hanzoai/static"
)

// securityHeaders stamps a baseline set of security headers on every
// response. The Permissions-Policy and Content-Security-Policy values
// are env-overridable for SPAs that legitimately need camera/microphone
// access (biometric KYC, video chat) or a looser CSP (inline scripts,
// data: URIs). Defaults are still locked-down; only an explicit
// HANZO_STATIC_PERMISSIONS_POLICY / HANZO_STATIC_CSP override widens
// them.
func securityHeaders(next http.Handler) http.Handler {
	permissions := envOr("HANZO_STATIC_PERMISSIONS_POLICY",
		"camera=(), microphone=(), geolocation=()")
	csp := envOr("HANZO_STATIC_CSP",
		"default-src 'none'; img-src 'self'; font-src 'self'; style-src 'self'")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", permissions)
		w.Header().Set("Content-Security-Policy", csp)
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

// writeRuntimeConfig templates /public/config.json from SPA_* env vars at
// startup. Every SPA_KEY=value env var becomes config.<camelCase(KEY)>=value.
// Numeric-looking values become JSON numbers, "true"/"false" booleans. If no
// SPA_* vars are set, the placeholder file shipped with the image is left
// untouched so the SPA can fall back to its own defaults. The file is written
// before the server starts serving so the first request already sees real
// config (no race).
func writeRuntimeConfig(root string) error {
	out := map[string]any{"v": 1}
	have := false
	for _, e := range os.Environ() {
		k, v, ok := strings.Cut(e, "=")
		if !ok || !strings.HasPrefix(k, "SPA_") || v == "" {
			continue
		}
		have = true
		key := toCamel(strings.TrimPrefix(k, "SPA_"))
		out[key] = parseValue(v)
	}
	if !have {
		log.Print("static: no SPA_* env vars set — leaving existing /config.json in place")
		return nil
	}
	// Deterministic key order for bundle-identical verification.
	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ordered := make([]byte, 0, 256)
	ordered = append(ordered, '{')
	for i, k := range keys {
		if i > 0 {
			ordered = append(ordered, ',')
		}
		kb, _ := json.Marshal(k)
		vb, _ := json.Marshal(out[k])
		ordered = append(ordered, kb...)
		ordered = append(ordered, ':')
		ordered = append(ordered, vb...)
	}
	ordered = append(ordered, '}', '\n')

	dst := filepath.Join(root, "config.json")
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, ordered, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmp, dst, err)
	}
	log.Printf("static: wrote runtime %s (%d keys)", dst, len(keys))
	return nil
}

// toCamel turns SPA_API_HOST into "apiHost". Multi-word keys use the first
// word lowercase, remaining words Title-cased.
func toCamel(s string) string {
	parts := strings.Split(strings.ToLower(s), "_")
	for i, p := range parts {
		if i == 0 || p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, "")
}

// parseValue turns env-var strings into the right JSON type. "true"/"false"
// become booleans, "[0-9]+" becomes a number, otherwise it stays a string.
// This is the minimal amount of coercion needed for chainId (int) and feature
// flags (bool); everything else is a string URL.
func parseValue(v string) any {
	switch strings.ToLower(v) {
	case "true":
		return true
	case "false":
		return false
	}
	if len(v) > 0 && v[0] >= '0' && v[0] <= '9' {
		allDigits := true
		for _, c := range v {
			if c < '0' || c > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			var n int64
			fmt.Sscan(v, &n)
			return n
		}
	}
	return v
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

	// Runtime config only applies to local-filesystem serving. S3-backed
	// deployments template their config.json upstream.
	if *s3Bucket == "" {
		if err := writeRuntimeConfig(*root); err != nil {
			log.Fatalf("static: runtime config: %v", err)
		}
	}

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
			UseSSL:    os.Getenv("S3_USE_SSL") == "true",
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

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("/", securityHeaders(handler))

	addr := fmt.Sprintf(":%d", *port)
	srv := &http.Server{
		Addr:           addr,
		Handler:        mux,
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   30 * time.Second,
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	log.Printf("static: serving %s on %s (spa=%v)", source, addr, *spa)

	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("static: %v", err)
		}
	}()

	<-done
	log.Print("static: shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("static: shutdown: %v", err)
	}
}
