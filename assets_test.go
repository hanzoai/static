package static_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	static "github.com/hanzoai/static"
)

// hashedSite lays out a static export the way Next emits one: an app shell at
// the root and content-addressed assets under /_next/static.
func hashedSite(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("index.html", "<html><body>app shell</body></html>")
	write("_next/static/chunks/webpack-cc208340efd468cd.js", "console.log(1)")
	write("_next/static/css/8b1a9953c4611296.css", "body{}")

	return dir
}

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost"+target, nil)
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	return rec
}

// A content-addressed asset is immutable.
//
// The bytes under /_next/static are named by a hash OF those bytes, so the file
// at a given URL can never change: a different build writes a different name.
// Revalidating it is therefore pure cost and pure risk — the request buys no new
// information, and if it lands after a deploy retired the old hash it turns a
// warm cache into a hard failure. `immutable` is the header that says so.
func TestContentHashedAssetIsImmutable(t *testing.T) {
	t.Parallel()

	cfg := static.CreateConfig()
	cfg.Root = hashedSite(t)

	handler, err := static.New(context.Background(), next(t), cfg, "static")
	if err != nil {
		t.Fatal(err)
	}

	for _, asset := range []string{
		"/_next/static/chunks/webpack-cc208340efd468cd.js",
		"/_next/static/css/8b1a9953c4611296.css",
	} {
		rec := get(t, handler, asset)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", asset, rec.Code)
		}

		got := rec.Header().Get("Cache-Control")
		if !strings.Contains(got, "immutable") {
			t.Errorf("%s: expected an immutable Cache-Control, got %q", asset, got)
		}
		if !strings.Contains(got, "max-age=31536000") {
			t.Errorf("%s: expected max-age=31536000, got %q", asset, got)
		}
	}
}

// The override configures the mutable files. It does not get to make a
// content-addressed asset revalidate, because for those the answer is not a
// matter of policy — the name is a hash of the bytes, so a stale hit is
// impossible and a revalidation is waste. Documented in the projects store as
// "content-hashed assets are always immutable regardless of this override".
func TestContentHashedAssetIsImmutableDespiteOverride(t *testing.T) {
	t.Parallel()

	cfg := static.CreateConfig()
	cfg.Root = hashedSite(t)
	cfg.CacheControl = map[string]string{
		".js": "max-age=600",
		"*":   "max-age=60",
	}

	handler, err := static.New(context.Background(), next(t), cfg, "static")
	if err != nil {
		t.Fatal(err)
	}

	rec := get(t, handler, "/_next/static/chunks/webpack-cc208340efd468cd.js")

	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("expected the override to be ignored for a hashed asset, got %q", got)
	}
}

// The document is the thing that has to change: it is what points at the new
// hashes, so freezing it would strand every client on the previous build. Only
// the hashed assets are immutable, never the shell.
func TestDocumentIsNotImmutable(t *testing.T) {
	t.Parallel()

	cfg := static.CreateConfig()
	cfg.Root = hashedSite(t)

	handler, err := static.New(context.Background(), next(t), cfg, "static")
	if err != nil {
		t.Fatal(err)
	}

	for _, doc := range []string{"/", "/index.html"} {
		rec := get(t, handler, doc)

		if got := rec.Header().Get("Cache-Control"); strings.Contains(got, "immutable") {
			t.Errorf("%s: the document must not be immutable, got %q", doc, got)
		}
	}
}

// A missing asset is a 404, not the app shell.
//
// This is the ChunkLoadError. The SPA fallback exists so an unmatched ROUTE
// reaches the client-side router; an asset is not a route. When a stale document
// asks for a chunk the current build no longer has, answering with index.html
// hands the browser HTML where it demanded JavaScript, and it dies in the parser
// with "Loading chunk N failed" instead of taking the clean, retryable 404.
func TestSPAFallbackDoesNotSwallowMissingAsset(t *testing.T) {
	t.Parallel()

	cfg := static.CreateConfig()
	cfg.Root = hashedSite(t)
	cfg.SPAMode = true

	handler, err := static.New(context.Background(), next(t), cfg, "static")
	if err != nil {
		t.Fatal(err)
	}

	// Under the content-addressed prefix, and a bare asset extension elsewhere.
	for _, miss := range []string{
		"/_next/static/chunks/9107-e634f55e46e597be.js",
		"/_next/static/css/deadbeefdeadbeef.css",
		"/assets/app-deadbeef.js",
		"/fonts/inter-c0ffee.woff2",
	} {
		rec := get(t, handler, miss)

		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: expected 404, got %d", miss, rec.Code)
		}

		body := rec.Body.String()
		if strings.Contains(strings.ToLower(body), "<html") {
			t.Errorf("%s: answered with an HTML body: %.80q", miss, body)
		}
		if ct := rec.Header().Get("Content-Type"); strings.Contains(ct, "text/html") {
			t.Errorf("%s: answered with Content-Type %q", miss, ct)
		}
		if len(body) > 1024 {
			t.Errorf("%s: expected a tiny body, got %d bytes", miss, len(body))
		}
	}
}

// The fallback still does its job for real routes — including a route with a dot
// in it, which is a path segment and not a file extension.
func TestSPAFallbackStillServesRoutes(t *testing.T) {
	t.Parallel()

	cfg := static.CreateConfig()
	cfg.Root = hashedSite(t)
	cfg.SPAMode = true

	handler, err := static.New(context.Background(), next(t), cfg, "static")
	if err != nil {
		t.Fatal(err)
	}

	for _, route := range []string{"/models/meta-llama", "/llm", "/referral", "/users/jane.doe"} {
		rec := get(t, handler, route)

		if rec.Code != http.StatusOK {
			t.Errorf("%s: expected the shell with 200, got %d", route, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "app shell") {
			t.Errorf("%s: expected the app shell, got %.80q", route, rec.Body.String())
		}
	}
}
