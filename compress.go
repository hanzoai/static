package static

import (
	"compress/gzip"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// gzipMinSize is the smallest body worth encoding. Below roughly one TCP
// segment the 18-byte gzip envelope and the round of CPU on both ends buy
// nothing, and a tiny body can come out larger than it went in.
const gzipMinSize = 1024

// gzipPool recycles encoders across requests so a busy server does not
// allocate a fresh 300 KB window per response.
var gzipPool = sync.Pool{
	New: func() any {
		w, _ := gzip.NewWriterLevel(nil, gzip.BestCompression)
		return w
	},
}

// acceptsGzip reports whether the client asked for gzip, honouring an
// explicit `gzip;q=0` refusal. Only gzip is negotiated: it is the one
// encoding every HTTP client has understood for twenty years, it is in the
// standard library, and so it costs this package no dependency and the Yaegi
// plugin build no third-party code to interpret.
func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
			continue
		}
		for _, p := range strings.Split(params, ";") {
			if k, v, ok := strings.Cut(p, "="); ok &&
				strings.EqualFold(strings.TrimSpace(k), "q") &&
				strings.TrimSpace(v) == "0" {
				return false
			}
		}
		return true
	}
	return false
}

// compressibleType reports whether a Content-Type is worth encoding. Markup,
// styles, scripts and JSON shrink by 70-90%; formats that are already
// compressed containers (raster images, woff2, video, archives, PDF) do not
// shrink and only cost CPU, so they are served as stored.
func compressibleType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	if strings.HasPrefix(ct, "text/") {
		return true
	}
	switch ct {
	case "application/javascript", "application/x-javascript",
		"application/json", "application/ld+json", "application/manifest+json",
		"application/xml", "application/xhtml+xml",
		"application/rss+xml", "application/atom+xml",
		"application/wasm", "image/svg+xml", "image/x-icon",
		"font/ttf", "font/otf", "application/vnd.ms-fontobject":
		return true
	}
	return false
}

// gzipWriter encodes a response body on the way out. The compress-or-not
// decision is deferred to the moment the status is known, because that is the
// first point at which the handler has finished declaring Content-Type and
// Content-Length — the two facts the decision turns on.
type gzipWriter struct {
	http.ResponseWriter
	gz      *gzip.Writer
	decided bool
}

func (g *gzipWriter) WriteHeader(status int) {
	g.decide(status)
	g.ResponseWriter.WriteHeader(status)
}

func (g *gzipWriter) Write(p []byte) (int, error) {
	// A handler that writes without WriteHeader has implied 200.
	g.decide(http.StatusOK)
	if g.gz != nil {
		return g.gz.Write(p)
	}
	return g.ResponseWriter.Write(p)
}

// Flush keeps streaming handlers streaming: the encoder has to be flushed
// first or its buffered bytes sit behind the connection flush.
func (g *gzipWriter) Flush() {
	if g.gz != nil {
		_ = g.gz.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (g *gzipWriter) decide(status int) {
	if g.decided {
		return
	}
	g.decided = true

	h := g.Header()
	// Vary regardless of the outcome. The same URL yields different bytes to
	// clients that do and do not send Accept-Encoding, and every cache between
	// here and the browser has to key on that.
	h.Add("Vary", "Accept-Encoding")

	switch {
	case status < http.StatusOK, // 1xx carries no body
		status == http.StatusNoContent,
		status == http.StatusNotModified,
		// 206 is addressed in bytes of the *stored* file; encoding it would
		// renumber the very offsets the client asked for.
		status == http.StatusPartialContent:
		return
	}
	if h.Get("Content-Encoding") != "" {
		return // already encoded upstream; leave it alone
	}
	if !compressibleType(h.Get("Content-Type")) {
		return
	}
	if n, err := strconv.Atoi(h.Get("Content-Length")); err == nil && n < gzipMinSize {
		return
	}

	h.Del("Content-Length") // the encoded length is not known until the end
	h.Del("Accept-Ranges")  // stored-byte offsets no longer address this body
	h.Set("Content-Encoding", "gzip")

	g.gz = gzipPool.Get().(*gzip.Writer)
	g.gz.Reset(g.ResponseWriter)
}

// close finishes the gzip stream and returns the encoder to the pool. It is
// safe to call when nothing was compressed.
func (g *gzipWriter) close() {
	if g.gz == nil {
		return
	}
	_ = g.gz.Close()
	gzipPool.Put(g.gz)
	g.gz = nil
}
