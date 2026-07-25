package static

import (
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

// serve builds a handler over a temp tree containing the given files.
func serve(t *testing.T, files map[string]string) http.Handler {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := CreateConfig()
	cfg.Root = root
	h, err := New(context.Background(), nil, cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func get(h http.Handler, path string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestGzipShrinksTextAndRoundTrips(t *testing.T) {
	body := strings.Repeat("the quick brown fox jumps over the lazy dog. ", 500)
	h := serve(t, map[string]string{"index.html": body})

	got := get(h, "/", map[string]string{"Accept-Encoding": "gzip"})
	if enc := got.Header().Get("Content-Encoding"); enc != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", enc)
	}
	if got.Body.Len() >= len(body) {
		t.Errorf("compressed size %d not smaller than %d", got.Body.Len(), len(body))
	}
	// The bytes must still be the file.
	zr, err := gzip.NewReader(got.Body)
	if err != nil {
		t.Fatal(err)
	}
	round, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if string(round) != body {
		t.Error("decompressed body does not match the file")
	}
	if v := got.Header().Get("Vary"); !strings.Contains(v, "Accept-Encoding") {
		t.Errorf("Vary = %q, want it to include Accept-Encoding", v)
	}
}

func TestPlainWhenClientDoesNotAskForGzip(t *testing.T) {
	h := serve(t, map[string]string{"index.html": "<html>hi</html>"})

	for _, ae := range []string{"", "identity", "gzip;q=0"} {
		got := get(h, "/", map[string]string{"Accept-Encoding": ae})
		if enc := got.Header().Get("Content-Encoding"); enc != "" {
			t.Errorf("Accept-Encoding %q: Content-Encoding = %q, want none", ae, enc)
		}
		if got.Body.String() != "<html>hi</html>" {
			t.Errorf("Accept-Encoding %q: body = %q", ae, got.Body.String())
		}
	}
}

func TestAlreadyCompressedTypesAreLeftAlone(t *testing.T) {
	h := serve(t, map[string]string{"logo.png": "\x89PNG\r\n\x1a\n not really a png"})

	got := get(h, "/logo.png", map[string]string{"Accept-Encoding": "gzip"})
	if enc := got.Header().Get("Content-Encoding"); enc != "" {
		t.Errorf("Content-Encoding = %q, want none for an image", enc)
	}
}

func TestCachePolicy(t *testing.T) {
	h := serve(t, map[string]string{
		"index.html":                  "<html></html>",
		"_next/static/chunks/a1b2.js": "console.log(1)",
		"data.txt":                    "plain",
	})

	cases := []struct {
		path, want string
	}{
		// A page must be revalidated, or it outlives the chunks it names.
		{"/", "no-cache"},
		// A build-hashed chunk can never change under that name.
		{"/_next/static/chunks/a1b2.js", "public, max-age=31536000, immutable"},
		{"/data.txt", "max-age=86400"},
	}
	for _, c := range cases {
		if got := get(h, c.path, nil).Header().Get("Cache-Control"); got != c.want {
			t.Errorf("%s: Cache-Control = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestETagAnswers304(t *testing.T) {
	h := serve(t, map[string]string{"index.html": "<html></html>"})

	first := get(h, "/", nil)
	tag := first.Header().Get("ETag")
	if tag == "" {
		t.Fatal("no ETag on the first response")
	}

	second := get(h, "/", map[string]string{"If-None-Match": tag})
	if second.Code != http.StatusNotModified {
		t.Errorf("status = %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Errorf("304 carried a %d-byte body", second.Body.Len())
	}
}
