package static_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	static "github.com/hanzoai/static"
)

// serveOne builds a handler over a temp dir holding the given files and runs
// one request through it.
func serveOne(t *testing.T, files map[string]string, target string, hdr http.Header) *httptest.ResponseRecorder {
	t.Helper()

	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cfg := static.CreateConfig()
	cfg.Root = dir
	handler, err := static.New(context.Background(), next(t), cfg, "static")
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost"+target, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func gunzip(t *testing.T, b []byte) string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("body is not a valid gzip stream: %v", err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gzip stream truncated: %v", err)
	}
	if err := zr.Close(); err != nil {
		t.Fatalf("gzip trailer bad: %v", err)
	}
	return string(out)
}

func TestGzipEncodesAndRoundTrips(t *testing.T) {
	t.Parallel()

	body := "<!doctype html><title>x</title>" + strings.Repeat("hello world ", 900)
	rec := serveOne(t, map[string]string{"index.html": body}, "/index.html",
		http.Header{"Accept-Encoding": {"gzip, deflate, br"}})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("expected Content-Encoding gzip, got %q", got)
	}
	if got := rec.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Fatalf("expected Vary: Accept-Encoding, got %q", got)
	}
	// A stale Content-Length would describe the file, not the stream, and the
	// client would truncate or hang.
	if got := rec.Header().Get("Content-Length"); got != "" {
		t.Fatalf("Content-Length must be dropped when encoding, got %q", got)
	}
	// Byte ranges address the stored file; they do not survive encoding.
	if got := rec.Header().Get("Accept-Ranges"); got != "" {
		t.Fatalf("Accept-Ranges must be dropped when encoding, got %q", got)
	}
	if out := gunzip(t, rec.Body.Bytes()); out != body {
		t.Fatalf("round trip lost bytes: got %d want %d", len(out), len(body))
	}
	if rec.Body.Len() >= len(body) {
		t.Fatalf("encoded body (%d) is not smaller than source (%d)", rec.Body.Len(), len(body))
	}
}

func TestGzipSkippedWithoutAcceptEncoding(t *testing.T) {
	t.Parallel()

	body := strings.Repeat("a", 4096)
	rec := serveOne(t, map[string]string{"index.html": body}, "/index.html", nil)

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("must not encode for a client that did not ask, got %q", got)
	}
	if rec.Body.String() != body {
		t.Fatal("identity body altered")
	}
}

func TestGzipHonoursQZero(t *testing.T) {
	t.Parallel()

	body := strings.Repeat("a", 4096)
	rec := serveOne(t, map[string]string{"index.html": body}, "/index.html",
		http.Header{"Accept-Encoding": {"gzip;q=0, identity"}})

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("gzip;q=0 is a refusal, got %q", got)
	}
}

func TestGzipSkipsIncompressibleAndTiny(t *testing.T) {
	t.Parallel()

	// Already-compressed container: encoding it costs CPU and gains nothing.
	rec := serveOne(t, map[string]string{"a.png": strings.Repeat("\x89PNG", 2000)}, "/a.png",
		http.Header{"Accept-Encoding": {"gzip"}})
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("png must not be encoded, got %q", got)
	}
	// Ranges must survive for the formats people actually seek in.
	if got := rec.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("expected byte ranges preserved on stored formats, got %q", got)
	}

	// Below one segment the envelope costs more than it saves.
	rec = serveOne(t, map[string]string{"t.html": "<p>hi</p>"}, "/t.html",
		http.Header{"Accept-Encoding": {"gzip"}})
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("tiny body must not be encoded, got %q", got)
	}
}

func TestGzipLeavesRangeRequestsAlone(t *testing.T) {
	t.Parallel()

	body := strings.Repeat("abcdefghij", 500)
	rec := serveOne(t, map[string]string{"index.html": body}, "/index.html",
		http.Header{"Accept-Encoding": {"gzip"}, "Range": {"bytes=0-9"}})

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("expected 206, got %d", rec.Code)
	}
	// Encoding a 206 would renumber the very offsets the client asked for.
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("206 must not be encoded, got %q", got)
	}
	if rec.Body.String() != "abcdefghij" {
		t.Fatalf("wrong range body %q", rec.Body.String())
	}
}

func TestGzipNotFoundPageIsEncodedAndStill404(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	page := "<html>" + strings.Repeat("<p>gone</p>", 400) + "</html>"
	if err := os.WriteFile(filepath.Join(dir, "404.html"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := static.CreateConfig()
	cfg.Root = dir
	cfg.ErrorPage404 = "404.html"
	handler, err := static.New(context.Background(), next(t), cfg, "static")
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost/nope", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("the error page is markup and should be encoded, got %q", got)
	}
	if out := gunzip(t, rec.Body.Bytes()); out != page {
		t.Fatal("404 body did not round trip")
	}
}

func TestGzipDirectoryListingIsEncoded(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for _, n := range []string{"alpha.txt", "beta.txt", "gamma.txt"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cfg := static.CreateConfig()
	cfg.Root = dir
	cfg.EnableDirectoryListing = true
	handler, err := static.New(context.Background(), next(t), cfg, "static")
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	// The listing streams through a template with no Content-Length, which is
	// the path where a lazily-decided encoder is easiest to get wrong.
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("expected the listing to be encoded, got %q", got)
	}
	if out := gunzip(t, rec.Body.Bytes()); !strings.Contains(out, "alpha.txt") {
		t.Fatalf("listing lost its content: %q", out)
	}
}

func TestGzipConditionalRequestStays304(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	p := filepath.Join(dir, "index.html")
	if err := os.WriteFile(p, []byte(strings.Repeat("z", 4096)), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := static.CreateConfig()
	cfg.Root = dir
	handler, err := static.New(context.Background(), next(t), cfg, "static")
	if err != nil {
		t.Fatal(err)
	}

	first := httptest.NewRecorder()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost/index.html", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept-Encoding", "gzip")
	handler.ServeHTTP(first, req)

	lm := first.Header().Get("Last-Modified")
	if lm == "" {
		t.Fatal("expected a Last-Modified validator on a stored file")
	}

	second := httptest.NewRecorder()
	req2, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost/index.html", nil)
	if err != nil {
		t.Fatal(err)
	}
	req2.Header.Set("Accept-Encoding", "gzip")
	req2.Header.Set("If-Modified-Since", lm)
	handler.ServeHTTP(second, req2)

	if second.Code != http.StatusNotModified {
		t.Fatalf("expected 304, got %d", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Fatalf("304 must carry no body, got %d bytes", second.Body.Len())
	}
	if got := second.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("304 must not claim an encoding, got %q", got)
	}
}
