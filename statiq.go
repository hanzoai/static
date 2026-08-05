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

// immutableCacheControl is the answer for bytes that cannot change.
const immutableCacheControl = "public, max-age=31536000, immutable"

// contentAddressedPrefixes are directories whose contents are named by a hash OF
// those contents. The build tool writes the hash into the filename, so the bytes
// at one of these URLs can never change — a different build produces a different
// name, and the old name simply stops being referenced. That is the whole
// argument for `immutable`, and it is why it is claimed HERE and nowhere else.
var contentAddressedPrefixes = []string{
	"/_next/static/", // Next.js
	"/_nuxt/",        // Nuxt
	"/_astro/",       // Astro
}

// assetExtensions mark a request for a SUBRESOURCE: the client wants these exact
// bytes and will hand them to a parser that is not HTML's. A route wants a
// document, which is what the SPA fallback is for — so this set is what tells the
// two apart on a miss.
//
// ".html" is deliberately absent: that IS a document. ".txt" is absent too, and
// less obviously — it is genuinely ambiguous (a served text file is as likely as
// a payload), and TestCustomErrorPage pins the branded page for a missing ".txt".
var assetExtensions = map[string]bool{
	".js": true, ".mjs": true, ".cjs": true, ".css": true, ".map": true,
	".json": true, ".wasm": true,
	".woff": true, ".woff2": true, ".ttf": true, ".otf": true, ".eot": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".svg": true,
	".webp": true, ".avif": true, ".ico": true,
	".mp4": true, ".webm": true, ".mp3": true, ".ogg": true, ".wav": true,
}

// isContentAddressed reports whether p names bytes under a content-addressed
// prefix. It matches the prefix anywhere in the path so that one function serves
// both spellings a path arrives in: the request URL ("/_next/static/chunks/x.js")
// and the root-relative path of the file actually being served.
func isContentAddressed(p string) bool {
	for _, prefix := range contentAddressedPrefixes {
		if strings.Contains(p, prefix) {
			return true
		}
	}

	return false
}

// isAsset reports whether a path asks for a subresource rather than a route.
func isAsset(urlPath string) bool {
	return isContentAddressed(urlPath) || assetExtensions[strings.ToLower(path.Ext(urlPath))]
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

// ServeHTTP serves HTTP requests with static files.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	upath := r.URL.Path
	if !strings.HasPrefix(upath, "/") {
		upath = "/" + upath
	}

	// servedPath is the root-relative path of the bytes that go out, which is not
	// always the path that was asked for — see the ".html" sibling just below. It
	// is what decides cacheability, so it has to track the file, not the request.
	servedPath := upath

	f, err := h.root.Open(upath)
	// Clean-URL fallback: an extensionless miss serves its ".html" sibling in
	// place (no redirect — the clean URL is canonical). This is the static-export
	// contract every exporter (Next, Astro, …) and host (Pages) shares:
	// /docs/flags is stored as docs/flags.html.
	if err != nil && os.IsNotExist(err) && !strings.HasSuffix(upath, "/") && path.Ext(upath) == "" {
		if hf, herr := h.root.Open(upath + ".html"); herr == nil {
			f, err = hf, nil
			servedPath = upath + ".html"
		}
	}
	if err != nil {
		if os.IsNotExist(err) {
			// A missing ASSET is missing. The fallbacks below both answer with a
			// document, and a document is the wrong kind of thing entirely when the
			// client asked for a subresource: the browser hands the HTML to a
			// JavaScript parser and dies there ("Loading chunk 8846 failed") instead
			// of taking the clean, retryable 404 the situation actually warrants.
			//
			// That is not hypothetical — it is the ChunkLoadError on this estate. A
			// client holding a document from the previous build asks for that build's
			// chunk hashes; the deploy retired them; SPA mode answered every one of
			// those with 200 and the app shell. The status was the lie that made an
			// ordinary cache miss unrecoverable.
			//
			// So this precedes both fallbacks, including the custom error page: a
			// branded HTML body is no better an answer to a request for a stylesheet,
			// it is only a more expensive one.
			if isAsset(upath) {
				http.NotFound(w, r)
				return
			}

			if h.spaMode {
				h.serveFile(w, r, filepath.Join(h.rootPath, h.spaIndex))
				return
			}

			if h.errorPage404 != "" {
				h.serveErrorPage(w, filepath.Join(h.rootPath, h.errorPage404))
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

		// No index file. Try the ".html" sibling BEFORE canonicalizing, because a
		// directory must not shadow a page of the same name.
		//
		// Next's App Router export emits both: the page at `skills.html` and a
		// `skills/` directory holding only its RSC payloads
		// (`__next.<id>.txt`) — no index.html. The open at the top of ServeHTTP
		// SUCCEEDS on that directory, so the extensionless ".html" fallback above
		// never runs (it is gated on IsNotExist), and without this the page is
		// unreachable at every spelling: /skills redirects to /skills/, which then
		// serves the 404 page. That is a WORSE answer than the nginx it replaces,
		// which at least served /skills via try_files $uri.html.
		sibling := strings.TrimSuffix(upath, "/") + ".html"
		if sf, serr := h.root.Open(sibling); serr == nil {
			sf.Close()
			h.serveFile(w, r, filepath.Join(h.rootPath, sibling))
			return
		}

		// No index file and no sibling: canonicalize to a trailing slash, then list (or 404).
		url := r.URL.Path
		if len(url) == 0 || url[len(url)-1] != '/' {
			localRedirect(w, r, url+"/")
			return
		}

		if !h.enableDirListing {
			if h.errorPage404 != "" {
				h.serveErrorPage(w, filepath.Join(h.rootPath, h.errorPage404))
				return
			}
			http.NotFound(w, r)
			return
		}

		h.serveDirectoryListing(w, r, f, d)
		return
	}

	h.setCacheHeaders(w, servedPath, d)

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

// setCacheHeaders writes the cache policy for servedPath, the root-relative path
// of the bytes going out.
func (h *Handler) setCacheHeaders(w http.ResponseWriter, servedPath string, d fs.FileInfo) {
	// A content-addressed asset is immutable, and says so ahead of any override.
	// Its name is a hash of its bytes, so the file at this URL cannot change and a
	// revalidation cannot learn anything: it is pure cost, and worse, pure risk —
	// a conditional request that lands after a deploy retired the hash turns a
	// warm cache into a hard failure. The override configures the MUTABLE files;
	// this is not a policy question, so it is not the override's to answer.
	if isContentAddressed(servedPath) {
		w.Header().Set("Cache-Control", immutableCacheControl)
		w.Header().Set("Last-Modified", d.ModTime().UTC().Format(http.TimeFormat))

		return
	}

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

// serveErrorPage writes a custom error page with the status it stands for.
//
// A 404 body served with 200 is a soft 404: the words say the page is gone while
// the status says it is fine. A crawler indexes it as a real page, a client cannot
// tell a typo from a hit, and every wrong URL on the site becomes a duplicate of
// whatever the error page contains. The status is the machine-readable half of the
// answer and it has to agree with the human half. Configuring ErrorPage404 used to
// FORCE 200 for exactly this reason and it was backwards — SPAMode already exists
// for the one case that legitimately answers 200 to an unknown path, because there
// a client-side router really will resolve it.
//
// It deliberately does not reuse serveFile. That path ends in http.ServeContent,
// which negotiates Range and If-Modified-Since and writes its own 200 — none of
// which an error body wants. The previous code kept ServeContent and tried to set
// the status by calling w.WriteHeader first, which cannot work: WriteHeader sends
// the header map, so the Content-Type serveFile set afterwards was discarded and
// net/http logged a superfluous WriteHeader call on every miss. Headers, then
// status, then body is the only order that produces a correct response.
func (h *Handler) serveErrorPage(w http.ResponseWriter, filePath string) {
	f, err := os.Open(filePath)
	if err != nil {
		// The configured page is missing. Still answer with the truth rather than
		// letting a misconfiguration turn a 404 into a 500.
		http.Error(w, "404 page not found", http.StatusNotFound)
		return
	}
	defer f.Close()

	if ct := mime.TypeByExtension(filepath.Ext(filePath)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	// Never cached as if it were the resource that was asked for: the same URL may
	// exist tomorrow, and a cached 404 would outlive the mistake.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.Copy(w, f)
}

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

	h.setCacheHeaders(w, h.servedPath(filePath), d)

	ext := filepath.Ext(d.Name())
	contentType := mime.TypeByExtension(ext)
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}

	http.ServeContent(w, r, d.Name(), d.ModTime(), f)
}

// servedPath turns the filesystem path of the bytes being served back into the
// root-relative path they answer to.
//
// The cache policy has to follow the FILE, not the request. The SPA fallback
// answers a route with index.html, so a request for a retired chunk would
// otherwise look content-addressed and freeze the app shell under that URL for a
// year — the failure this whole change exists to remove, reintroduced by keying
// on the wrong path.
func (h *Handler) servedPath(filePath string) string {
	rel := strings.TrimPrefix(filepath.ToSlash(filePath), filepath.ToSlash(h.rootPath))
	if !strings.HasPrefix(rel, "/") {
		rel = "/" + rel
	}

	return rel
}

func localRedirect(w http.ResponseWriter, r *http.Request, newPath string) {
	if q := r.URL.RawQuery; q != "" {
		newPath += "?" + q
	}
	w.Header().Set("Location", newPath)
	w.WriteHeader(http.StatusMovedPermanently)
}
