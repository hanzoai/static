package static

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
)

// serveDir stands up the real file handler over a temp root, wrapped exactly as
// cmd/static wraps it. Every assertion below therefore describes the shipped
// composition, not a mock of it.
func serveDir(t *testing.T, files map[string]string) http.Handler {
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
	h, err := New(context.Background(), http.NotFoundHandler(), &Config{
		Root:       dir,
		IndexFiles: []string{"index.html"},
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	return Compress(h)
}

func get(h http.Handler, path string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestGzipRoundTrip(t *testing.T) {
	body := strings.Repeat("<p>the same paragraph, many times over</p>\n", 400) // 16.8 KB
	h := serveDir(t, map[string]string{"index.html": body})

	w := get(h, "/index.html", map[string]string{"Accept-Encoding": "gzip, deflate, br"})

	if got := w.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := w.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Errorf("Vary = %q, want it to name Accept-Encoding", got)
	}
	if got := w.Header().Get("Content-Length"); got != "" {
		t.Errorf("Content-Length = %q — the identity length must not survive", got)
	}
	if got := w.Header().Get("Accept-Ranges"); got != "" {
		t.Errorf("Accept-Ranges = %q — identity offsets do not address a gzip stream", got)
	}

	// Snapshot the wire bytes BEFORE decompressing: gzip.NewReader(w.Body)
	// drains the buffer, and a length read afterwards is 0 — which passes a
	// "smaller than the original" assertion for the wrong reason.
	wire := append([]byte(nil), w.Body.Bytes()...)

	zr, err := gzip.NewReader(bytes.NewReader(wire))
	if err != nil {
		t.Fatalf("body is not gzip: %v", err)
	}
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Fatal("decompressed body differs from the file on disk")
	}
	if len(wire) == 0 || len(wire) >= len(body) {
		t.Fatalf("compressed %d bytes is not smaller than %d", len(wire), len(body))
	}
	t.Logf("html %d -> %d bytes (%.0f%% off)", len(body), len(wire),
		100*(1-float64(len(wire))/float64(len(body))))
}

func TestIdentityWhenNotAccepted(t *testing.T) {
	body := strings.Repeat("x", 40000)
	h := serveDir(t, map[string]string{"a.css": body})

	w := get(h, "/a.css", nil)
	if got := w.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q with no Accept-Encoding", got)
	}
	if w.Body.String() != body {
		t.Error("body was altered for a client that asked for nothing")
	}
}

// `gzip;q=0` is the one spelling of "gzip" that means no, and a substring test
// gets it backwards.
func TestGzipQZeroIsRefusal(t *testing.T) {
	h := serveDir(t, map[string]string{"a.js": strings.Repeat("x", 40000)})
	w := get(h, "/a.js", map[string]string{"Accept-Encoding": "gzip;q=0, identity"})
	if got := w.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q for gzip;q=0", got)
	}
}

// A range of the compressed stream is not the range that was asked for.
func TestRangeStaysIdentity(t *testing.T) {
	body := strings.Repeat("abcdefgh", 5000)
	h := serveDir(t, map[string]string{"a.txt": body})

	w := get(h, "/a.txt", map[string]string{"Accept-Encoding": "gzip", "Range": "bytes=0-7"})
	if got := w.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q on a Range request", got)
	}
	if w.Code != http.StatusPartialContent {
		t.Errorf("status = %d, want 206", w.Code)
	}
	if w.Body.String() != "abcdefgh" {
		t.Errorf("body = %q, want the first 8 bytes", w.Body.String())
	}
}

// Re-compressing a PNG spends CPU to produce more bytes than it started with.
func TestAlreadyCompressedTypesUntouched(t *testing.T) {
	png := "\x89PNG\r\n\x1a\n" + strings.Repeat("\x00\x01\x02\x03", 8000)
	h := serveDir(t, map[string]string{"a.png": png})
	w := get(h, "/a.png", map[string]string{"Accept-Encoding": "gzip"})
	if got := w.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q for image/png", got)
	}
	if got := w.Header().Get("Vary"); strings.Contains(got, "Accept-Encoding") {
		t.Errorf("Vary = %q — a type that never varies must not fragment caches", got)
	}
}

// Under one MTU the frame count is the same either way, and gzip's 18-byte
// envelope can make the answer bigger.
func TestTinyBodiesUntouched(t *testing.T) {
	h := serveDir(t, map[string]string{"a.html": "<p>hi</p>"})
	w := get(h, "/a.html", map[string]string{"Accept-Encoding": "gzip"})
	if got := w.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q for a 9-byte body", got)
	}
	if w.Body.String() != "<p>hi</p>" {
		t.Errorf("body = %q", w.Body.String())
	}
}

// A 304 carries no body. Writing a gzip header and an empty member onto one
// produces a response no client can parse.
func TestNotModifiedCarriesNoEncoding(t *testing.T) {
	h := serveDir(t, map[string]string{"a.html": strings.Repeat("<p>x</p>", 4000)})

	first := get(h, "/a.html", map[string]string{"Accept-Encoding": "gzip"})
	lm := first.Header().Get("Last-Modified")
	if lm == "" {
		t.Fatal("no Last-Modified to revalidate against")
	}

	w := get(h, "/a.html", map[string]string{"Accept-Encoding": "gzip", "If-Modified-Since": lm})
	if w.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", w.Code)
	}
	if got := w.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q on a 304", got)
	}
	if w.Body.Len() != 0 {
		t.Errorf("304 wrote %d body bytes", w.Body.Len())
	}
}

// The 404 page is a handler that writes its own status and streams its body —
// a different path through the wrapper than ServeContent takes.
func TestErrorPageCompresses(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("<p>not found</p>\n", 500)
	if err := os.WriteFile(filepath.Join(dir, "404.html"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := New(context.Background(), http.NotFoundHandler(), &Config{
		Root: dir, IndexFiles: []string{"index.html"}, ErrorPage404: "404.html",
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	w := get(Compress(h), "/nope", map[string]string{"Accept-Encoding": "gzip"})

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if got := w.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	zr, err := gzip.NewReader(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatalf("404 body is not gzip: %v", err)
	}
	got, _ := io.ReadAll(zr)
	if string(got) != body {
		t.Error("404 body did not survive the round trip")
	}
}

// A HEAD carries no body, so its Content-Length must describe the resource. The
// first cut of this wrapper let gzip close over the empty stream and net/http
// counted the 20-byte envelope: a 146 KB stylesheet advertised 23 bytes.
func TestHeadAdvertisesTheResourceLength(t *testing.T) {
	body := strings.Repeat("<p>x</p>\n", 5000) // 45000 bytes
	h := serveDir(t, map[string]string{"a.html": body})

	r := httptest.NewRequest(http.MethodHead, "/a.html", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if got := w.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q on a HEAD", got)
	}
	if got := w.Header().Get("Content-Length"); got != "45000" {
		t.Errorf("Content-Length = %q, want 45000 (the resource, not a gzip envelope)", got)
	}
}

func TestCompressibleClassification(t *testing.T) {
	yes := []string{
		"text/html; charset=utf-8", "text/css", "application/javascript",
		"text/javascript; charset=utf-8", "application/json", "image/svg+xml",
		"application/manifest+json", "application/wasm", "TEXT/PLAIN",
	}
	no := []string{
		"image/png", "image/jpeg", "font/woff2", "video/mp4",
		"application/gzip", "application/octet-stream", "",
	}
	for _, ct := range yes {
		if !compressible(ct) {
			t.Errorf("compressible(%q) = false", ct)
		}
	}
	for _, ct := range no {
		if compressible(ct) {
			t.Errorf("compressible(%q) = true", ct)
		}
	}
}
