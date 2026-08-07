package static

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testKey = "pk-abc123"

func want(t *testing.T, got, sub string) {
	t.Helper()
	if !strings.Contains(got, sub) {
		t.Fatalf("missing %q in:\n%s", sub, got)
	}
}

func TestInjectOnce(t *testing.T) {
	doc := []byte("<html><head><title>x</title></head><body>hi</body></html>")

	out := string(inject(doc, testKey))
	want(t, out, `<script defer src="https://api.hanzo.ai/v1/event.js" data-key="pk-abc123"></script></head>`)

	if n := strings.Count(out, "/v1/event.js"); n != 1 {
		t.Fatalf("injected %d tags, want 1", n)
	}
	// Re-injecting is a no-op: a document that already carries the tag — because
	// the edge tagged it, or because the site pasted it in — must not get a second.
	if again := string(inject([]byte(out), testKey)); again != out {
		t.Fatalf("second inject changed the document:\n%s", again)
	}
}

func TestInjectLeavesDocumentAlone(t *testing.T) {
	for _, c := range []struct{ name, doc, key string }{
		{"no head", "<html><body>hi</body></html>", testKey},
		{"fragment", "just text", testKey},
		{"empty", "", testKey},
		{"no key", "<html><head></head></html>", ""},
		{"key is not publishable", "<html><head></head></html>", "sk-secret"},
		{"key is unquotable", "<html><head></head></html>", `pk-"><script>`},
	} {
		if got := string(inject([]byte(c.doc), c.key)); got != c.doc {
			t.Errorf("%s: changed the document to %q", c.name, got)
		}
	}
}

func TestInjectFindsUppercaseHead(t *testing.T) {
	out := string(inject([]byte("<HTML><HEAD></HEAD><BODY></BODY></HTML>"), testKey))
	want(t, out, `data-key="pk-abc123"></script></HEAD>`)
}

// serveKeyed serves dir through a handler holding key, and returns the response
// for path.
func serveKeyed(t *testing.T, dir, key, path string) *httptest.ResponseRecorder {
	t.Helper()

	h := handler(t, &Config{
		Root:       dir,
		Key:        key,
		IndexFiles: []string{"index.html"},
		SPAMode:    true,
		SPAIndex:   "index.html",
	})

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))

	return w
}

func TestServeTagsDocuments(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "index.html", "<html><head></head><body>home</body></html>")
	write(t, dir, "bare.html", "<html><body>no head</body></html>")
	write(t, dir, "app.js", "console.log('<head></head>')")
	write(t, dir, "data.json", `{"head":"</head>"}`)

	tag := `<script defer src="https://api.hanzo.ai/v1/event.js" data-key="pk-abc123"></script>`

	t.Run("document", func(t *testing.T) {
		w := serveKeyed(t, dir, testKey, "/index.html")
		body := w.Body.String()
		want(t, body, tag+"</head>")
		if n := strings.Count(body, "/v1/event.js"); n != 1 {
			t.Fatalf("served %d tags, want 1", n)
		}
		if got, err := os.ReadFile(filepath.Join(dir, "index.html")); err != nil || bytes.Contains(got, []byte("event.js")) {
			t.Fatal("the file on disk was modified")
		}
	})

	t.Run("spa route", func(t *testing.T) {
		want(t, serveKeyed(t, dir, testKey, "/some/client/route").Body.String(), tag)
	})

	t.Run("directory", func(t *testing.T) {
		want(t, serveKeyed(t, dir, testKey, "/").Body.String(), tag)
	})

	// Everything that is not a document is served byte-for-byte, including files
	// whose contents happen to spell </head>.
	for _, path := range []string{"/app.js", "/data.json", "/bare.html"} {
		t.Run("untouched "+path, func(t *testing.T) {
			body := serveKeyed(t, dir, testKey, path).Body.String()
			if strings.Contains(body, "event.js") {
				t.Fatalf("%s was tagged:\n%s", path, body)
			}
			on, err := os.ReadFile(filepath.Join(dir, strings.TrimPrefix(path, "/")))
			if err != nil {
				t.Fatal(err)
			}
			if body != string(on) {
				t.Fatalf("%s was rewritten:\n%s", path, body)
			}
		})
	}

	t.Run("no key", func(t *testing.T) {
		w := serveKeyed(t, dir, "", "/index.html")
		if strings.Contains(w.Body.String(), "event.js") {
			t.Fatal("tagged without a key")
		}
	})
}

func TestServeTaggedLengthMatchesBody(t *testing.T) {
	dir := t.TempDir()
	doc := "<html><head></head><body>home</body></html>"
	write(t, dir, "index.html", doc)

	w := serveKeyed(t, dir, testKey, "/index.html")
	if got, n := w.Header().Get("Content-Length"), w.Body.Len(); got != "" && got != itoa(n) {
		t.Fatalf("Content-Length %s, body %d bytes", got, n)
	}
	if w.Body.Len() <= len(doc) {
		t.Fatalf("body is %d bytes, no tag was added", w.Body.Len())
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}

	return string(b)
}

// The live policies on docs, cloud and console — the sites this actually has to
// work on — plus the shapes around them.
func TestAdmit(t *testing.T) {
	for _, c := range []struct{ name, in, out string }{
		{
			"docs.hanzo.ai names the origin only for connect",
			"default-src 'self'; script-src 'self' 'unsafe-inline' blob: https://hanzo.app; connect-src 'self' https://api.hanzo.ai https://hanzo.app",
			"default-src 'self'; script-src 'self' 'unsafe-inline' blob: https://hanzo.app https://api.hanzo.ai; connect-src 'self' https://api.hanzo.ai https://hanzo.app",
		},
		{
			"cloud.hanzo.ai covers connect by wildcard host",
			"default-src 'self'; script-src 'self' https://hanzo.app; connect-src 'self' https://*.hanzo.ai",
			"default-src 'self'; script-src 'self' https://hanzo.app https://api.hanzo.ai; connect-src 'self' https://*.hanzo.ai",
		},
		{
			"console.hanzo.ai needs the script side only",
			"default-src 'self'; script-src 'self' 'unsafe-inline'; connect-src 'self' https://hanzo.id https://api.hanzo.ai; object-src 'none'",
			"default-src 'self'; script-src 'self' 'unsafe-inline' https://api.hanzo.ai; connect-src 'self' https://hanzo.id https://api.hanzo.ai; object-src 'none'",
		},
		{
			"an absent directive inherits default-src, spelled out",
			"default-src 'self' https://cdn.example",
			"default-src 'self' https://cdn.example; script-src 'self' https://cdn.example https://api.hanzo.ai; connect-src 'self' https://cdn.example https://api.hanzo.ai",
		},
		{
			"a policy that governs neither is left alone",
			"frame-ancestors 'none'",
			"frame-ancestors 'none'",
		},
		{"already allowed outright", "script-src https://api.hanzo.ai; connect-src https://api.hanzo.ai", "script-src https://api.hanzo.ai; connect-src https://api.hanzo.ai"},
		{"allowed by scheme", "default-src https:", "default-src https:"},
		{"allowed by star", "script-src *; connect-src *", "script-src *; connect-src *"},
		{"a wildcard that only ends in the same letters does not cover", "script-src https://*.evilhanzo.ai; connect-src *", "script-src https://*.evilhanzo.ai https://api.hanzo.ai; connect-src *"},
		{"no policy", "", ""},
		{"blank parts do not panic", " ; ;; ", " ; ;; "},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := http.Header{}
			if c.in != "" {
				h.Set("Content-Security-Policy", c.in)
			}
			admit(h)
			if got := h.Get("Content-Security-Policy"); got != c.out {
				t.Errorf("\n got %q\nwant %q", got, c.out)
			}
		})
	}
}

func TestAdmitOnlyForATagWeAdded(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "index.html", "<html><head></head><body></body></html>")
	write(t, dir, "bare.html", "<html><body></body></html>")
	write(t, dir, "app.js", "x")

	policy := "default-src 'self'; script-src 'self'; connect-src 'self'"
	serve := func(key, path string) string {
		h := handler(t, &Config{Root: dir, Key: key, IndexFiles: []string{"index.html"}})
		w := httptest.NewRecorder()
		w.Header().Set("Content-Security-Policy", policy)
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))

		return w.Header().Get("Content-Security-Policy")
	}

	if got := serve(testKey, "/index.html"); !strings.Contains(got, "script-src 'self' https://api.hanzo.ai") {
		t.Errorf("tagged document did not admit the origin: %s", got)
	}
	for _, c := range []struct{ key, path string }{{testKey, "/bare.html"}, {testKey, "/app.js"}, {"", "/index.html"}} {
		if got := serve(c.key, c.path); got != policy {
			t.Errorf("key=%q %s: policy widened without a tag: %s", c.key, c.path, got)
		}
	}
}

// A 404 page is a document the site serves, and the pageview on it is the only
// way a broken link anywhere on the web reports itself.
func TestErrorPageIsTagged(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "index.html", "<html><head></head><body>home</body></html>")
	write(t, dir, "404.html", "<html><head></head><body>not here</body></html>")

	h := handler(t, &Config{Root: dir, Key: testKey, IndexFiles: []string{"index.html"}, ErrorPage404: "404.html"})

	w := httptest.NewRecorder()
	w.Header().Set("Content-Security-Policy", "default-src 'self'")
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/nope-xyz", nil))

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	want(t, w.Body.String(), `data-key="pk-abc123"`)
	want(t, w.Header().Get("Content-Security-Policy"), "script-src 'self' https://api.hanzo.ai")
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
}
