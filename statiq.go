// Package static provides a static file server middleware for Hanzo Ingress.
package static

import (
	"compress/gzip"
	"context"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Config holds the static file server middleware configuration.
type Config struct {
	// Root directory to serve files from (local filesystem).
	Root string `json:"root,omitempty"`

	// S3 backend configuration (if set, takes precedence over Root).
	S3 *S3Config `json:"s3,omitempty"`

	// EnableDirectoryListing enables directory listing.
	EnableDirectoryListing bool `json:"enableDirectoryListing,omitempty"`

	// IndexFiles is a list of filenames to try when a directory is requested.
	IndexFiles []string `json:"indexFiles,omitempty"`

	// SPAMode redirects all not-found requests to a single page.
	SPAMode bool `json:"spaMode,omitempty"`

	// SPAIndex is the file to serve in SPA mode.
	SPAIndex string `json:"spaIndex,omitempty"`

	// ErrorPage404 is the path to a custom 404 error page.
	ErrorPage404 string `json:"errorPage404,omitempty"`

	// CacheControl sets cache control headers for static files.
	CacheControl map[string]string `json:"cacheControl,omitempty"`
}

// CreateConfig creates the default plugin configuration.
func CreateConfig() *Config {
	return &Config{
		Root:                   ".",
		EnableDirectoryListing: false,
		IndexFiles:             []string{"index.html", "index.htm"},
		SPAMode:                false,
		SPAIndex:               "index.html",
		ErrorPage404:           "",
		CacheControl:           map[string]string{},
	}
}

// dirEntry represents a file or directory for the directory listing template.
type dirEntry struct {
	Name    string
	Size    int64
	Mode    os.FileMode
	ModTime time.Time
	IsDir   bool
}

func init() {
	mime.AddExtensionType(".go", "text/x-go")
}

// Handler is a static file server handler.
type Handler struct {
	root                 http.FileSystem
	rootPath             string
	enableDirListing     bool
	indexFiles           []string
	spaMode              bool
	spaIndex             string
	errorPage404         string
	cacheControl         map[string]string
	notFoundResponseCode int
	name                 string
}

// New creates a new static file server middleware.
func New(ctx context.Context, next http.Handler, config *Config, name string) (http.Handler, error) {
	var rootFS http.FileSystem
	var rootPath string

	if config.S3 != nil && config.S3.Bucket != "" {
		s3fs, err := NewS3FS(ctx, *config.S3)
		if err != nil {
			return nil, fmt.Errorf("s3 backend: %w", err)
		}
		rootFS = s3fs
		rootPath = "s3://" + config.S3.Bucket
	} else {
		root, err := filepath.Abs(config.Root)
		if err != nil {
			return nil, fmt.Errorf("invalid root path: %w", err)
		}
		if _, err := os.Stat(root); os.IsNotExist(err) {
			if err := os.MkdirAll(root, 0755); err != nil {
				return nil, fmt.Errorf("failed to create root directory: %w", err)
			}
		}
		rootFS = http.Dir(root)
		rootPath = root
	}

	notFoundResponseCode := http.StatusNotFound
	if config.ErrorPage404 != "" {
		notFoundResponseCode = http.StatusOK
	}

	return &Handler{
		root:                 rootFS,
		rootPath:             rootPath,
		enableDirListing:     config.EnableDirectoryListing,
		indexFiles:           config.IndexFiles,
		spaMode:              config.SPAMode,
		spaIndex:             config.SPAIndex,
		errorPage404:         config.ErrorPage404,
		cacheControl:         config.CacheControl,
		notFoundResponseCode: notFoundResponseCode,
		name:                 name,
	}, nil
}

// GetTracingInformation returns the middleware name and type for observability.
func (h *Handler) GetTracingInformation() (string, string) {
	return h.name, "Static"
}

// ServeHTTP serves HTTP requests with static files.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	upath := r.URL.Path
	if !strings.HasPrefix(upath, "/") {
		upath = "/" + upath
	}

	f, err := h.root.Open(upath)
	// Clean-URL fallback: an extensionless miss serves a same-named sibling in
	// place (no redirect — the clean URL is canonical), trying ".html" then ".js".
	// ".html" is the static-export contract every exporter (Next, Astro, …) and
	// host (Pages) shares: /docs/flags is stored as docs/flags.html. ".js" serves
	// embed endpoints at their clean path with the correct text/javascript type —
	// X-Content-Type-Options: nosniff makes the type load-bearing, so a.hanzo.ai/chat
	// (stored as chat.js) must resolve to a .js file, not a sniffed text/plain one.
	if err != nil && os.IsNotExist(err) && !strings.HasSuffix(upath, "/") && path.Ext(upath) == "" {
		for _, ext := range []string{".html", ".js"} {
			if hf, herr := h.root.Open(upath + ext); herr == nil {
				f, err = hf, nil
				break
			}
		}
	}
	if err != nil {
		if os.IsNotExist(err) {
			if h.spaMode {
				h.serveFile(w, r, filepath.Join(h.rootPath, h.spaIndex))
				return
			}

			if h.errorPage404 != "" {
				w.WriteHeader(h.notFoundResponseCode)
				h.serveFile(w, r, filepath.Join(h.rootPath, h.errorPage404))
				return
			}

			http.NotFound(w, r)
			return
		}
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	defer f.Close()

	d, err := f.Stat()
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	if d.IsDir() {
		// Serve the directory's index IN PLACE — no redirect — for BOTH /pricing and
		// /pricing/. This matches the clean-URL contract this server already applies to
		// flat ".html" siblings above (the requested path is canonical). It also kills
		// the old needless double redirect: a materialized directory index from a static
		// export (/pricing → /pricing/ → /pricing/index.html) now resolves in ZERO
		// redirects, and the internal "index.html" filename never leaks into the address
		// bar. Static exports use absolute asset URLs, so serving at the slash-less path
		// is safe.
		for _, index := range h.indexFiles {
			indexPath := path.Join(upath, index)
			if indexFile, ierr := h.root.Open(indexPath); ierr == nil {
				indexFile.Close()
				h.serveFile(w, r, filepath.Join(h.rootPath, indexPath))
				return
			}
		}

		// No index file: canonicalize to a trailing slash, then list (or 404).
		url := r.URL.Path
		if len(url) == 0 || url[len(url)-1] != '/' {
			localRedirect(w, r, url+"/")
			return
		}

		if !h.enableDirListing {
			if h.errorPage404 != "" {
				w.WriteHeader(h.notFoundResponseCode)
				h.serveFile(w, r, filepath.Join(h.rootPath, h.errorPage404))
				return
			}
			http.NotFound(w, r)
			return
		}

		h.serveDirectoryListing(w, r, f, d)
		return
	}

	h.writeFile(w, r, d, f.(io.ReadSeeker))
}

func (h *Handler) serveDirectoryListing(w http.ResponseWriter, r *http.Request, f http.File, d fs.FileInfo) {
	dirs, err := f.Readdir(-1)
	if err != nil {
		http.Error(w, "Error reading directory", http.StatusInternalServerError)
		return
	}

	sort.Slice(dirs, func(i, j int) bool {
		if dirs[i].IsDir() && !dirs[j].IsDir() {
			return true
		}
		if !dirs[i].IsDir() && dirs[j].IsDir() {
			return false
		}
		return dirs[i].Name() < dirs[j].Name()
	})

	entries := make([]dirEntry, len(dirs))
	for i, entry := range dirs {
		entries[i] = dirEntry{
			Name:    entry.Name(),
			Size:    entry.Size(),
			Mode:    entry.Mode(),
			ModTime: entry.ModTime(),
			IsDir:   entry.IsDir(),
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	tmpl := template.Must(template.New("dirlist").Parse(`
<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <title>Index of {{.Path}}</title>
    <style>
        body { font-family: sans-serif; margin: 2em; }
        table { border-collapse: collapse; width: 100%; }
        th, td { text-align: left; padding: 8px; }
        tr:nth-child(even) { background-color: #f2f2f2; }
        th { background-color: #333; color: white; }
        a { text-decoration: none; }
        a:hover { text-decoration: underline; }
    </style>
</head>
<body>
    <h1>Index of {{.Path}}</h1>
    <table>
        <tr>
            <th>Name</th>
            <th>Size</th>
            <th>Modified</th>
        </tr>
        {{if ne .Path "/"}}
        <tr>
            <td><a href="../">../</a></td>
            <td>-</td>
            <td>-</td>
        </tr>
        {{end}}
        {{range .Files}}
        <tr>
            <td><a href="{{.Name}}{{if .IsDir}}/{{end}}">{{.Name}}{{if .IsDir}}/{{end}}</a></td>
            <td>{{if .IsDir}}-{{else}}{{.Size}} bytes{{end}}</td>
            <td>{{.ModTime.Format "2006-01-02 15:04:05"}}</td>
        </tr>
        {{end}}
    </table>
</body>
</html>
`))

	data := struct {
		Path  string
		Files []dirEntry
	}{
		Path:  r.URL.Path,
		Files: entries,
	}

	if err = tmpl.Execute(w, data); err != nil {
		http.Error(w, "Error rendering directory listing", http.StatusInternalServerError)
	}
}

// immutable reports whether a URL path addresses build-hashed content, which
// can never change under that name. A framework writes a fresh filename for
// every build, so the old one is only ever requested by a page that still
// wants the old bytes.
func immutable(urlPath string) bool {
	return strings.Contains(urlPath, "/_next/static/") ||
		strings.Contains(urlPath, "/_app/immutable/")
}

func (h *Handler) setCacheHeaders(w http.ResponseWriter, r *http.Request, d fs.FileInfo) {
	ext := filepath.Ext(d.Name())

	switch {
	case h.cacheControl[ext] != "":
		w.Header().Set("Cache-Control", h.cacheControl[ext])
	case h.cacheControl["*"] != "":
		w.Header().Set("Cache-Control", h.cacheControl["*"])
	case immutable(r.URL.Path):
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	case ext == ".html" || ext == ".htm":
		// A page must never outlive the hashed assets it names: serving stale
		// HTML from cache asks for chunks the current deploy has deleted, and
		// the site renders blank. Revalidating is cheap — it answers 304.
		w.Header().Set("Cache-Control", "no-cache")
	default:
		w.Header().Set("Cache-Control", "max-age=86400")
	}

	w.Header().Set("Last-Modified", d.ModTime().UTC().Format(http.TimeFormat))
	w.Header().Set("ETag", etag(d))
}

// etag identifies a version of a file. Size and mtime change together whenever
// the content does, which is what a validator has to promise.
func etag(d fs.FileInfo) string {
	return fmt.Sprintf(`"%x-%x"`, d.ModTime().UnixNano(), d.Size())
}

// compressible reports whether a content type is worth gzipping. Media and
// archives are already compressed; running them through gzip spends CPU to
// make them marginally larger.
func compressible(contentType string) bool {
	ct := strings.ToLower(contentType)
	if strings.HasPrefix(ct, "text/") {
		return true
	}
	for _, s := range []string{"javascript", "json", "xml", "svg", "wasm", "font/ttf", "font/otf", "x-font"} {
		if strings.Contains(ct, s) {
			return true
		}
	}
	return false
}

// gzipWriter sends the body compressed. Content-Length is deliberately absent:
// the length is not known until the last byte is compressed.
type gzipWriter struct {
	http.ResponseWriter
	gz *gzip.Writer
}

func (w gzipWriter) Write(b []byte) (int, error) { return w.gz.Write(b) }

func (h *Handler) serveFile(w http.ResponseWriter, r *http.Request, filePath string) {
	f, err := os.Open(filePath)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	d, err := f.Stat()
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	h.writeFile(w, r, d, f)
}

// writeFile is the one path a file body takes out of this server: validators,
// content type, then the bytes — compressed when the client asked for that and
// the type gains from it.
func (h *Handler) writeFile(w http.ResponseWriter, r *http.Request, d fs.FileInfo, content io.ReadSeeker) {
	h.setCacheHeaders(w, r, d)

	contentType := mime.TypeByExtension(filepath.Ext(d.Name()))
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}

	// Whether a client gets the compressed or the plain bytes depends on what
	// it asked for, so a shared cache has to key on that.
	w.Header().Add("Vary", "Accept-Encoding")

	// The client already holds this version.
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, etag(d)) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	// A range names offsets into the stored bytes, so a ranged request is
	// answered uncompressed.
	if r.Header.Get("Range") == "" && compressible(contentType) && acceptsGzip(r) {
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		_, _ = io.Copy(gzipWriter{ResponseWriter: w, gz: gz}, content)
		return
	}

	http.ServeContent(w, r, d.Name(), d.ModTime(), content)
}

// acceptsGzip reports whether the client offered gzip. A bare "gzip;q=0"
// declines it.
func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(fields[0]), "gzip") {
			for _, p := range fields[1:] {
				if strings.EqualFold(strings.TrimSpace(p), "q=0") {
					return false
				}
			}
			return true
		}
	}
	return false
}

func localRedirect(w http.ResponseWriter, r *http.Request, newPath string) {
	if q := r.URL.RawQuery; q != "" {
		newPath += "?" + q
	}
	w.Header().Set("Location", newPath)
	w.WriteHeader(http.StatusMovedPermanently)
}
