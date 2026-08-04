package static

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"sync"
)

// compressMinBytes is the floor below which gzip is not worth it. A frame under
// one MTU costs the same one packet compressed or not, and gzip's own header
// plus trailer is 18 bytes — so a 200-byte body can come back LARGER. The
// declared Content-Length is what we test; when there is none (a streamed body)
// we compress, because the streamed bodies here are HTML pages.
const compressMinBytes = 1024

// compressible answers whether a Content-Type is text under the skin. Anything
// already entropy-coded — jpeg, png, woff2, gzip, mp4 — comes back BIGGER for
// the CPU spent, which is why this is an allowlist and not a denylist.
//
// The suffix rules carry most of the weight: `+json` and `+xml` cover the long
// tail (image/svg+xml, application/manifest+json) without naming each one.
func compressible(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	if strings.HasPrefix(ct, "text/") ||
		strings.HasSuffix(ct, "+json") ||
		strings.HasSuffix(ct, "+xml") ||
		strings.HasSuffix(ct, "+text") {
		return true
	}
	switch ct {
	case "application/javascript", "application/x-javascript", "application/ecmascript",
		"application/json", "application/xml", "application/wasm",
		"application/manifest+json", "application/rss+xml", "application/atom+xml",
		"application/x-ndjson", "font/ttf", "font/otf", "image/x-icon", "image/vnd.microsoft.icon":
		return true
	}
	return false
}

// acceptsGzip reports whether the client asked for gzip AND did not disqualify
// it with `gzip;q=0`. A bare substring test says yes to "gzip;q=0", which is the
// one spelling that means no.
func acceptsGzip(h string) bool {
	for _, part := range strings.Split(h, ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
			continue
		}
		for _, p := range strings.Split(params, ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
			if ok && strings.EqualFold(strings.TrimSpace(k), "q") && strings.TrimSpace(v) == "0" {
				return false
			}
		}
		return true
	}
	return false
}

// gzipPool reuses the ~800 KB window a gzip.Writer allocates. Level 6 (the
// default), not 9: on this site's own payload 9 buys under 2% more over 6 and
// costs roughly triple the CPU per request, and nothing here is cached
// server-side, so every request pays the compress.
var gzipPool = sync.Pool{New: func() any {
	w, _ := gzip.NewWriterLevel(io.Discard, gzip.DefaultCompression)
	return w
}}

// gzipWriter decides at WriteHeader — the first moment the Content-Type is
// known, because the handler sets it from the file extension just before — and
// only then reaches for a compressor. Deciding earlier would mean guessing from
// the URL; deciding later would mean the header is already on the wire.
type gzipWriter struct {
	http.ResponseWriter
	gz      *gzip.Writer
	decided bool
}

func (w *gzipWriter) WriteHeader(status int) {
	if w.decided {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.decided = true

	h := w.Header()
	// Vary goes on EVERY compressible response, including the ones this call
	// decides not to compress. A shared cache that stored the identity copy
	// without it would serve those bytes to a client that asked for gzip and,
	// worse, the gzip copy to one that cannot read it.
	if compressible(h.Get("Content-Type")) {
		h.Add("Vary", "Accept-Encoding")
	}

	switch {
	// 204 and 304 have no body by definition; 206 is a byte range, and a range
	// of the COMPRESSED stream is not the range that was asked for. Range
	// requests never get this far anyway — the wrapper is not installed for
	// them — but a handler may still answer 206 on its own.
	case status == http.StatusNoContent || status == http.StatusNotModified || status == http.StatusPartialContent:
	case h.Get("Content-Encoding") != "":
	case !compressible(h.Get("Content-Type")):
	case declaredSize(h.Get("Content-Length")) < compressMinBytes:
	default:
		h.Set("Content-Encoding", "gzip")
		// The length of the identity body is a lie about the body that follows.
		// Deleting it is what puts the response on chunked transfer (HTTP/1.1)
		// or a length-free DATA stream (HTTP/2).
		h.Del("Content-Length")
		// Byte offsets into the identity body no longer address this body.
		h.Del("Accept-Ranges")
		w.gz = gzipPool.Get().(*gzip.Writer)
		w.gz.Reset(w.ResponseWriter)
	}
	w.ResponseWriter.WriteHeader(status)
}

// declaredSize turns a Content-Length header into a number, treating "absent"
// as "large". A streamed body declares no length, and refusing to compress it
// on that ground would exempt exactly the biggest responses.
func declaredSize(v string) int64 {
	if v == "" {
		return 1 << 62
	}
	var n int64
	for _, c := range v {
		if c < '0' || c > '9' {
			return 1 << 62
		}
		n = n*10 + int64(c-'0')
	}
	return n
}

func (w *gzipWriter) Write(b []byte) (int, error) {
	if !w.decided {
		w.WriteHeader(http.StatusOK)
	}
	if w.gz != nil {
		return w.gz.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

// Flush keeps server-sent events and any progressive HTML working: without it
// the wrapper swallows the interface and the bytes sit in the gzip window until
// the handler returns.
func (w *gzipWriter) Flush() {
	if w.gz != nil {
		_ = w.gz.Flush()
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *gzipWriter) close() {
	if w.gz == nil {
		return
	}
	_ = w.gz.Close()
	w.gz.Reset(io.Discard) // drop the reference to the real connection
	gzipPool.Put(w.gz)
	w.gz = nil
}

// Compress wraps a handler so text responses go out gzip-encoded when the client
// says it can read them.
//
// It is deliberately OUTSIDE the file server rather than inside it: the decision
// is about the response, not about the file, and every handler mounted here —
// the directory listing, the 404 page, a future one — gets it for free.
//
// Three requests opt out entirely and are handed the raw writer:
//
//   - No `Accept-Encoding: gzip`. Nothing to negotiate.
//   - A `Range` request. http.ServeContent answers those from byte offsets into
//     the identity body; those offsets do not address a compressed stream, and
//     re-deriving them would mean compressing the whole file to serve a slice of
//     it. Ranges stay identity, which is what every CDN and browser expects.
//   - A HEAD. It has no body, so there is nothing to compress, and its headers
//     describe the identity representation. Wrapping it was measurably wrong:
//     net/http discards a HEAD body but still counts the bytes offered, so the
//     gzip envelope closing over an empty stream made the server announce
//     `Content-Length: 23` for a 146 KB stylesheet — a number no GET would ever
//     produce and one a health check or a CDN would believe.
func Compress(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead || r.Header.Get("Range") != "" ||
			!acceptsGzip(r.Header.Get("Accept-Encoding")) {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipWriter{ResponseWriter: w}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}
