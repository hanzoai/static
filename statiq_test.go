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

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost/subdir/", nil)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusMovedPermanently {
		t.Errorf("Expected redirect for directory, got status code %d", recorder.Code)
	}

	location := recorder.Header().Get("Location")
	if location != "/subdir/custom.html" {
		t.Errorf("Expected redirect to /subdir/custom.html, got %s", location)
	}

	req, err = http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost"+location, nil)
	if err != nil {
		t.Fatal(err)
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Errorf("Expected 200 OK, got %d", recorder.Code)
	}

	if recorder.Body.String() != indexContent {
		t.Errorf("Expected index content, got %s", recorder.Body.String())
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

	if recorder.Code != http.StatusOK {
		t.Errorf("Expected 200 OK for custom error page, got %d", recorder.Code)
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
