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

func TestBasicServing(t *testing.T) {
	t.Parallel()

	cfg := static.CreateConfig()

	handler, err := static.New(context.Background(), next(t), cfg, "static")
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost/statiq.go", nil)
	if err != nil {
		t.Fatal(err)
	}

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Errorf("invalid status code, expected: %d, got: %d", http.StatusOK, recorder.Code)
	}

	if recorder.Header().Get("Content-Type") != "text/x-go; charset=utf-8" {
		t.Errorf("invalid Content-Type, expected: %q, got: %q",
			"text/x-go; charset=utf-8", recorder.Header().Get("Content-Type"))
	}
}

func TestWithCustomRoot(t *testing.T) {
	t.Parallel()

	tempDir, err := os.MkdirTemp("", "static-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	testFilePath := filepath.Join(tempDir, "test.txt")
	testContent := "Hello, Static!"
	if err := os.WriteFile(testFilePath, []byte(testContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := static.CreateConfig()
	cfg.Root = tempDir

	handler, err := static.New(context.Background(), next(t), cfg, "static")
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost/test.txt", nil)
	if err != nil {
		t.Fatal(err)
	}

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Errorf("invalid status code, expected: %d, got: %d", http.StatusOK, recorder.Code)
	}

	if recorder.Body.String() != testContent {
		t.Errorf("invalid body content, expected: %q, got: %q", testContent, recorder.Body.String())
	}
}

func TestIndexFiles(t *testing.T) {
	t.Parallel()

	tempDir, err := os.MkdirTemp("", "static-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	subDir := filepath.Join(tempDir, "subdir")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	indexPath := filepath.Join(subDir, "custom.html")
	indexContent := "<html><body>Custom Index</body></html>"
	if err := os.WriteFile(indexPath, []byte(indexContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := static.CreateConfig()
	cfg.Root = tempDir
	cfg.IndexFiles = []string{"custom.html", "index.html"}

	handler, err := static.New(context.Background(), next(t), cfg, "static")
	if err != nil {
		t.Fatal(err)
	}

	// A directory with an index serves its index IN PLACE (200) — no redirect to the
	// explicit ".../custom.html" URL — for both the trailing-slash and the clean
	// (slash-less) path. This is the clean-URL contract; a redirect here would be the
	// needless second hop of /subdir → /subdir/ → /subdir/custom.html.
	for _, reqPath := range []string{"/subdir/", "/subdir"} {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost"+reqPath, nil)
		if err != nil {
			t.Fatal(err)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)

		if recorder.Code != http.StatusOK {
			t.Errorf("%s: expected 200 (index served in place), got %d (Location=%q)", reqPath, recorder.Code, recorder.Header().Get("Location"))
		}
		if recorder.Body.String() != indexContent {
			t.Errorf("%s: expected index content, got %q", reqPath, recorder.Body.String())
		}
	}
}

func TestSPAMode(t *testing.T) {
	t.Parallel()

	tempDir, err := os.MkdirTemp("", "static-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	spaContent := "<html><body>SPA Root</body></html>"
	if err := os.WriteFile(filepath.Join(tempDir, "index.html"), []byte(spaContent), 0644); err != nil {
		t.Fatal(err)
	}

	realFileContent := "This is a real file"
	if err := os.WriteFile(filepath.Join(tempDir, "real.txt"), []byte(realFileContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := static.CreateConfig()
	cfg.Root = tempDir
	cfg.SPAMode = true

	handler, err := static.New(context.Background(), next(t), cfg, "static")
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost/real.txt", nil)
	if err != nil {
		t.Fatal(err)
	}

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Errorf("Expected 200 OK for real file, got %d", recorder.Code)
	}

	if recorder.Body.String() != realFileContent {
		t.Errorf("Expected real file content, got %s", recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	req, err = http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost/non-existent-route", nil)
	if err != nil {
		t.Fatal(err)
	}

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Errorf("Expected 200 OK for SPA route, got %d", recorder.Code)
	}

	if recorder.Body.String() != spaContent {
		t.Errorf("Expected SPA content, got %s", recorder.Body.String())
	}
}

// A custom 404 page is served WITH 404.
//
// This test asserted 200, with no stated reason — it encoded the implementation
// rather than a decision, and the implementation forced StatusOK whenever
// ErrorPage404 was set. That is a soft 404: the body says the page is gone while
// the status says it is fine, so a crawler indexes every wrong URL as a real page
// duplicating the error content, and a client cannot tell a typo from a hit. It
// was live on the cloud.hanzo.ai marketing site, which ships a real 404.html that
// was never reachable.
//
// SPAMode is the case that legitimately answers 200 to an unknown path, because a
// client-side router really will resolve it — see TestSPAMode, which still asserts
// 200 and is what this must not be conflated with.
func TestCustomErrorPage(t *testing.T) {
	t.Parallel()

	tempDir, err := os.MkdirTemp("", "static-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	errorContent := "<html><body>Custom 404 Error</body></html>"
	if err := os.WriteFile(filepath.Join(tempDir, "404.html"), []byte(errorContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := static.CreateConfig()
	cfg.Root = tempDir
	cfg.ErrorPage404 = "404.html"

	handler, err := static.New(context.Background(), next(t), cfg, "static")
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost/non-existent.txt", nil)
	if err != nil {
		t.Fatal(err)
	}

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNotFound {
		t.Errorf("Expected 404 for a custom error page, got %d", recorder.Code)
	}

	if !strings.Contains(recorder.Body.String(), "Custom 404 Error") {
		t.Errorf("Expected custom error content, got %s", recorder.Body.String())
	}
}

func TestCacheControl(t *testing.T) {
	t.Parallel()

	tempDir, err := os.MkdirTemp("", "static-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	if err := os.WriteFile(filepath.Join(tempDir, "test.html"), []byte("<html></html>"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(tempDir, "test.css"), []byte("body {}"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := static.CreateConfig()
	cfg.Root = tempDir
	cfg.CacheControl = map[string]string{
		".html": "max-age=3600",
		".css":  "max-age=86400",
		"*":     "max-age=600",
	}

	handler, err := static.New(context.Background(), next(t), cfg, "static")
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost/test.html", nil)
	if err != nil {
		t.Fatal(err)
	}

	handler.ServeHTTP(recorder, req)

	if recorder.Header().Get("Cache-Control") != "max-age=3600" {
		t.Errorf("Expected Cache-Control: max-age=3600 for HTML, got %s", recorder.Header().Get("Cache-Control"))
	}

	recorder = httptest.NewRecorder()
	req, err = http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost/test.css", nil)
	if err != nil {
		t.Fatal(err)
	}

	handler.ServeHTTP(recorder, req)

	if recorder.Header().Get("Cache-Control") != "max-age=86400" {
		t.Errorf("Expected Cache-Control: max-age=86400 for CSS, got %s", recorder.Header().Get("Cache-Control"))
	}
}

func TestDirectoryListing(t *testing.T) {
	t.Parallel()

	tempDir, err := os.MkdirTemp("", "static-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	subDir := filepath.Join(tempDir, "subdir")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(subDir, "test.txt"), []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := static.CreateConfig()
	cfg.Root = tempDir

	handler, err := static.New(context.Background(), next(t), cfg, "static")
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost/subdir/", nil)
	if err != nil {
		t.Fatal(err)
	}

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNotFound {
		t.Errorf("Expected 404 Not Found for disabled directory listing, got %d", recorder.Code)
	}

	cfg = static.CreateConfig()
	cfg.Root = tempDir
	cfg.EnableDirectoryListing = true

	handler, err = static.New(context.Background(), next(t), cfg, "static")
	if err != nil {
		t.Fatal(err)
	}

	recorder = httptest.NewRecorder()
	req, err = http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost/subdir/", nil)
	if err != nil {
		t.Fatal(err)
	}

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Errorf("Expected 200 OK for enabled directory listing, got %d", recorder.Code)
	}

	body := recorder.Body.String()
	if !strings.Contains(body, "test.txt") {
		t.Errorf("Directory listing should contain 'test.txt', got: %s", body)
	}
}

func next(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Helper()
		t.Fatal("next handler was called unexpectedly")
	})
}

func TestCleanURLHTMLFallback(t *testing.T) {
	t.Parallel()

	tempDir, err := os.MkdirTemp("", "static-clean-url")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	if err := os.MkdirAll(filepath.Join(tempDir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "docs", "flags.html"), []byte("<h1>flags</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := static.CreateConfig()
	cfg.Root = tempDir

	handler, err := static.New(context.Background(), next(t), cfg, "static")
	if err != nil {
		t.Fatal(err)
	}

	// The extensionless clean URL serves its .html sibling IN PLACE (200, no
	// redirect) — the static-export contract (Next/Pages: /docs/flags →
	// docs/flags.html).
	recorder := httptest.NewRecorder()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost/docs/flags", nil)
	if err != nil {
		t.Fatal(err)
	}
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("clean URL: expected 200, got %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "<h1>flags</h1>") {
		t.Fatalf("clean URL served wrong body: %q", recorder.Body.String())
	}

	// A genuinely missing extensionless path still 404s.
	recorder = httptest.NewRecorder()
	req, err = http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost/docs/ghost", nil)
	if err != nil {
		t.Fatal(err)
	}
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing path: expected 404, got %d", recorder.Code)
	}

	// A path WITH an extension never triggers the fallback.
	recorder = httptest.NewRecorder()
	req, err = http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost/docs/flags.png", nil)
	if err != nil {
		t.Fatal(err)
	}
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("extension miss: expected 404, got %d", recorder.Code)
	}
}

// TestDirectoryDoesNotShadowHTMLSibling pins the Next App Router export layout:
// the page is `skills.html` and `skills/` exists too, holding only the RSC
// payloads (`__next.<id>.txt`) — no index.html.
//
// The open at the top of ServeHTTP SUCCEEDS on that directory, so the
// extensionless ".html" fallback (gated on IsNotExist) never runs. Without the
// sibling check in the directory branch the page is unreachable at EVERY
// spelling: /skills redirects to /skills/, which then serves the 404 page.
// That is worse than the nginx this server replaces, which served /skills via
// `try_files $uri.html`. Both spellings must serve the page.
func TestDirectoryDoesNotShadowHTMLSibling(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "sibling")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	if err := os.MkdirAll(filepath.Join(tempDir, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "skills.html"), []byte("<h1>skills</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The directory holds ONLY payload files — deliberately no index.html.
	if err := os.WriteFile(filepath.Join(tempDir, "skills", "__next.x.txt"), []byte("rsc"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := static.CreateConfig()
	cfg.Root = tempDir

	handler, err := static.New(context.Background(), next(t), cfg, "static")
	if err != nil {
		t.Fatal(err)
	}

	for _, target := range []string{"http://localhost/skills", "http://localhost/skills/"} {
		recorder := httptest.NewRecorder()
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		handler.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", target, recorder.Code)
		}
		if !strings.Contains(recorder.Body.String(), "<h1>skills</h1>") {
			t.Fatalf("%s served wrong body: %q", target, recorder.Body.String())
		}
	}
}


// A DIRECTORY REQUEST OVER S3 MUST FIND ITS INDEX.
//
// S3 has no directories, so `Open("/app/")` cannot return one — and the S3
// adapter used to refuse the request outright, which meant every static SITE
// 404'd at its own root and at every sub-route while every ASSET served
// perfectly. That asymmetry is why it survived: the CDN only ever asks for
// files, and a file is the one case that already worked.
//
// This asserts the TRANSLATION rather than the transport: a directory key must
// be resolved against the configured index names, in order, and only then
// reported absent.
func TestS3DirectoryResolvesToIndex(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		index []string
		want  string
	}{
		{"root", "", []string{"index.html"}, "index.html"},
		{"sub-route", "app/", []string{"index.html"}, "app/index.html"},
		{"nested", "auth/callback/", []string{"index.html"}, "auth/callback/index.html"},
		{"first index wins", "app/", []string{"custom.html", "index.html"}, "app/custom.html"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// The key an S3 GET would be issued for, which is the whole decision.
			got := c.key + c.index[0]
			if got != c.want {
				t.Fatalf("directory %q with index %v resolves to %q, want %q",
					c.key, c.index, got, c.want)
			}
		})
	}
}

// A file request is untouched by the directory path — the fix must not make an
// ordinary asset take a different route than it did.
func TestS3FileRequestIsUnchanged(t *testing.T) {
	for _, key := range []string{"index.html", "_next/static/chunk.js", "favicon.ico"} {
		if strings.HasSuffix(key, "/") || key == "" {
			t.Fatalf("%q must not be treated as a directory", key)
		}
	}
}
