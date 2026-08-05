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

// A DOCUMENT MUST ARRIVE AS A DOCUMENT.
//
// commerce.hanzo.ai went down for the length of one deploy because every page
// came back `Content-Type: application/x-gzip` with no `Content-Encoding: gzip`.
// This server also sets `X-Content-Type-Options: nosniff`, so the browser was
// handed what it read as a gzip download and refused to render it — while the
// BODY was byte-for-byte correct. Nothing that checks a status code, a byte
// count or a payload hash can see that: the page is right and only its framing
// is wrong, so the failure is invisible to every check except this one.
//
// These pin the framing itself. `Content-Type` is derived from the file
// extension (mime.TypeByExtension), so the way this recurs is a served path
// whose extension is not the document's — a `.gz` sibling picked up by a
// resolution change, a pre-compressed asset served as a file. The test does not
// care WHICH mistake was made; it asserts what a browser needs to be true.
func TestHTMLIsFramedAsADocument(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><title>ok</title>"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := static.CreateConfig()
	cfg.Root = dir

	handler, err := static.New(context.Background(), next(t), cfg, "static")
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/", "/index.html"} {
		rec := httptest.NewRecorder()
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost"+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		// A browser always asks for compression; the bug only appeared for a
		// client that did.
		req.Header.Set("Accept-Encoding", "gzip, deflate, br")
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d, want 200", path, rec.Code)
		}

		ct := rec.Header().Get("Content-Type")
		if !strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s: Content-Type %q — a browser with nosniff will refuse to render this; want text/html", path, ct)
		}

		// The pairing is the point: a compressed body is only legible when the
		// encoding is DECLARED. Uncompressed is fine; compressed-and-silent is
		// the outage.
		if enc := rec.Header().Get("Content-Encoding"); enc != "" && enc != "identity" {
			if !strings.HasPrefix(ct, "text/html") {
				t.Errorf("%s: Content-Encoding %q with Content-Type %q — the body is unreadable", path, enc, ct)
			}
		}

		if got := rec.Body.String(); !strings.Contains(got, "<!doctype html") {
			t.Errorf("%s: body is not the document (first bytes %q) — it may be a compressed stream served as a file",
				path, got[:min(24, len(got))])
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
