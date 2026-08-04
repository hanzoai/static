// Package static provides a static file server middleware for Hanzo Ingress.
package static

import (
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
	root             http.FileSystem
	rootPath         string
	enableDirListing bool
	indexFiles       []string
	spaMode          bool
	spaIndex         string
	errorPage404     string
	cacheControl     map[string]string
	name             string
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

	return &Handler{
		root:             rootFS,
		rootPath:         rootPath,
		enableDirListing: config.EnableDirectoryListing,
		indexFiles:       config.IndexFiles,
		spaMode:          config.SPAMode,
		spaIndex:         config.SPAIndex,
		errorPage404:     config.ErrorPage404,
		cacheControl:     config.CacheControl,
		name:             name,
	}, nil
}

// GetTracingInformation returns the middleware name and type for observability.
func (h *Handler) GetTracingInformation() (string, string) {
	return h.name, "Static"
}

// ServeHTTP serves HTTP requests with static files, encoding the response
// when the client accepts it. Negotiation wraps every path below — files,
// directory listings, the SPA index and the 404 page — so there is one place
// in this server that decides how bytes go on the wire.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if acceptsGzip(r) {
		gw := &gzipWriter{ResponseWriter: w}
		defer gw.close()
		w = gw
	}
	h.serve(w, r)
}

func (h *Handler) serve(w http.ResponseWriter, r *http.Request) {
	upath := r.URL.Path
	if !strings.HasPrefix(upath, "/") {
		upath = "/" + upath
	}

	f, err := h.root.Open(upath)
	// Clean-URL fallback: an extensionless miss serves its ".html" sibling in
	// place (no redirect — the clean URL is canonical). This is the static-export
	// contract every exporter (Next, Astro, …) and host (Pages) shares:
	// /docs/flags is stored as docs/flags.html.
	if err != nil && os.IsNotExist(err) && !strings.HasSuffix(upath, "/") && path.Ext(upath) == "" {
		if hf, herr := h.root.Open(upath + ".html"); herr == nil {
			f, err = hf, nil
		}
	}
	if err != nil {
		if os.IsNotExist(err) {
			if h.spaMode {
				// A client-side router owns the path; 200 is the whole point.
				h.serveFile(w, r, filepath.Join(h.rootPath, h.spaIndex), http.StatusOK)
				return
			}

			if h.errorPage404 != "" {
				h.serveFile(w, r, filepath.Join(h.rootPath, h.errorPage404), http.StatusNotFound)
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
				h.serveFile(w, r, filepath.Join(h.rootPath, indexPath), http.StatusOK)
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
				h.serveFile(w, r, filepath.Join(h.rootPath, h.errorPage404), http.StatusNotFound)
				return
			}
			http.NotFound(w, r)
			return
		}

		h.serveDirectoryListing(w, r, f, d)
		return
	}

	h.setCacheHeaders(w, r, d)

	name := d.Name()
	ext := filepath.Ext(name)
	contentType := mime.TypeByExtension(ext)
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}

	http.ServeContent(w, r, d.Name(), d.ModTime(), f.(io.ReadSeeker))
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

func (h *Handler) setCacheHeaders(w http.ResponseWriter, _ *http.Request, d fs.FileInfo) {
	ext := filepath.Ext(d.Name())

	if maxAge, ok := h.cacheControl[ext]; ok {
		w.Header().Set("Cache-Control", maxAge)
	} else if maxAge, ok := h.cacheControl["*"]; ok {
		w.Header().Set("Cache-Control", maxAge)
	} else {
		w.Header().Set("Cache-Control", "max-age=86400")
	}

	w.Header().Set("Last-Modified", d.ModTime().UTC().Format(http.TimeFormat))
}

// serveFile writes filePath's contents under the given status. Every header
// is set before the body is handed to http.ServeContent, because ServeContent
// is what calls WriteHeader — anything stamped after it is silently dropped.
func (h *Handler) serveFile(w http.ResponseWriter, r *http.Request, filePath string, status int) {
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

	modTime := d.ModTime()
	if status == http.StatusOK {
		h.setCacheHeaders(w, r, d)
	} else {
		// An error body must not be cached — the path it stands in for may
		// come into existence at any time — and it carries no validator, so
		// ServeContent is given a zero mtime and skips Last-Modified/304.
		w.Header().Set("Cache-Control", "no-store")
		modTime = time.Time{}
		w = &statusOverride{ResponseWriter: w, code: status}
	}

	ext := filepath.Ext(d.Name())
	contentType := mime.TypeByExtension(ext)
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}

	http.ServeContent(w, r, d.Name(), modTime, f)
}

// statusOverride rewrites the 200 that http.ServeContent always writes for a
// full body into the status the caller actually meant. Any other status
// ServeContent chooses for itself (416 on an unsatisfiable range) is its own
// decision about the transfer and passes through untouched.
type statusOverride struct {
	http.ResponseWriter
	code int
}

func (s *statusOverride) WriteHeader(status int) {
	if status == http.StatusOK {
		status = s.code
	}
	s.ResponseWriter.WriteHeader(status)
}

func localRedirect(w http.ResponseWriter, r *http.Request, newPath string) {
	if q := r.URL.RawQuery; q != "" {
		newPath += "?" + q
	}
	w.Header().Set("Location", newPath)
	w.WriteHeader(http.StatusMovedPermanently)
}
