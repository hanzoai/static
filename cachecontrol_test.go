package static

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// An object that declares its own Cache-Control wins over the per-extension
// defaults, so a versioned font path keeps its immutable lifetime.
func TestObjectCacheControlWins(t *testing.T) {
	t.Parallel()

	h := &Handler{cacheControl: map[string]string{".woff2": "max-age=600", "*": "max-age=86400"}}
	info := &s3FileInfo{
		name:         "GeistVariable.woff2",
		mod:          time.Now(),
		cacheControl: "public, max-age=31536000, immutable",
	}

	w := httptest.NewRecorder()
	h.setCacheHeaders(w, httptest.NewRequest(http.MethodGet, "/fonts/geist/1.7.2/GeistVariable.woff2", nil), info)

	if got := w.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("object Cache-Control should win, got %q", got)
	}
}

// An object with no Cache-Control of its own falls back to the configured
// per-extension value, then "*", then the built-in default.
func TestCacheControlFallbackOrder(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		cfg  map[string]string
		want string
	}{
		{"extension", map[string]string{".woff2": "max-age=600", "*": "max-age=99"}, "max-age=600"},
		{"wildcard", map[string]string{"*": "max-age=99"}, "max-age=99"},
		{"builtin default", map[string]string{}, "max-age=86400"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := &Handler{cacheControl: tc.cfg}
			info := &s3FileInfo{name: "GeistVariable.woff2", mod: time.Now()}

			w := httptest.NewRecorder()
			h.setCacheHeaders(w, httptest.NewRequest(http.MethodGet, "/x.woff2", nil), info)

			if got := w.Header().Get("Cache-Control"); got != tc.want {
				t.Errorf("want %q, got %q", tc.want, got)
			}
		})
	}
}

// A local-filesystem FileInfo carries no policy of its own and must not be
// mistaken for one that does.
func TestLocalFileInfoHasNoObjectPolicy(t *testing.T) {
	t.Parallel()

	h := &Handler{cacheControl: map[string]string{"*": "max-age=99"}}
	info := &s3FileInfo{name: "a.css", mod: time.Now()} // empty cacheControl

	if _, ok := interface{}(info).(cacheControlled); !ok {
		t.Fatal("s3FileInfo must implement cacheControlled")
	}

	w := httptest.NewRecorder()
	h.setCacheHeaders(w, httptest.NewRequest(http.MethodGet, "/a.css", nil), info)

	if got := w.Header().Get("Cache-Control"); got != "max-age=99" {
		t.Errorf("empty object policy must fall through, got %q", got)
	}
}
