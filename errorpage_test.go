package static

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A custom 404 page must be served WITH 404. Serving it with 200 is a soft 404:
// the body says the page is gone while the status says it is fine, so a crawler
// indexes every wrong URL as a real page duplicating the error content, and a
// client cannot tell a typo from a hit. Configuring ErrorPage404 used to force
// StatusOK, which is what made this worth pinning rather than assuming.
func TestErrorPageKeepsItsStatus(t *testing.T) {
	root := t.TempDir()
	write(t, root, "index.html", "<h1>home</h1>")
	write(t, root, "404.html", "<h1>not here</h1>")

	h := handler(t, &Config{Root: root, IndexFiles: []string{"index.html"}, ErrorPage404: "404.html"})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope-xyz", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 — a 404 body served with %d is a soft 404", rec.Code, rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "not here") {
		t.Errorf("body = %q, want the configured 404 page", body)
	}
	// The header must survive: the previous code called WriteHeader BEFORE the file
	// helper set Content-Type, so the type was dropped and the browser had to sniff.
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html — headers must be set before the status", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store — a cached 404 outlives the mistake", cc)
	}
}

// The one case that legitimately answers 200 to an unknown path: a client-side
// router really will resolve it. This is what ErrorPage404 must NOT be conflated
// with, so the two are pinned against each other.
func TestSPAModeStillAnswers200(t *testing.T) {
	root := t.TempDir()
	write(t, root, "index.html", "<h1>app shell</h1>")

	h := handler(t, &Config{Root: root, IndexFiles: []string{"index.html"}, SPAMode: true, SPAIndex: "index.html"})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/deep/client/route", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("SPA status = %d, want 200 — the client router resolves this path", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "app shell") {
		t.Error("SPA mode must serve the shell for an unknown path")
	}
}

// With no error page configured at all, a miss is still a plain 404.
func TestMissWithoutErrorPageIs404(t *testing.T) {
	root := t.TempDir()
	write(t, root, "index.html", "<h1>home</h1>")

	h := handler(t, &Config{Root: root, IndexFiles: []string{"index.html"}})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope-xyz", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// A misconfigured page (named but absent) must still answer 404, not 500 — the
// request really was for something that does not exist, and our own config
// mistake is not the caller's problem.
func TestMissingErrorPageStillAnswers404(t *testing.T) {
	root := t.TempDir()
	write(t, root, "index.html", "<h1>home</h1>")

	h := handler(t, &Config{Root: root, IndexFiles: []string{"index.html"}, ErrorPage404: "does-not-exist.html"})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope-xyz", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 even when the configured page is missing", rec.Code)
	}
}

// A real page must be unaffected — the fix must not turn hits into misses.
func TestExistingPageUnaffected(t *testing.T) {
	root := t.TempDir()
	write(t, root, "index.html", "<h1>home</h1>")
	write(t, root, "404.html", "<h1>not here</h1>")
	write(t, root, "pricing.html", "<h1>pricing</h1>")

	h := handler(t, &Config{Root: root, IndexFiles: []string{"index.html"}, ErrorPage404: "404.html"})

	for _, p := range []string{"/", "/pricing", "/pricing.html"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200", p, rec.Code)
		}
	}
}

func handler(t *testing.T, c *Config) http.Handler {
	t.Helper()
	h, err := New(context.Background(), http.NotFoundHandler(), c, "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

func write(t *testing.T, root, name, body string) {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

var _ = io.Discard
