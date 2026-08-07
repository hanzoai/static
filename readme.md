<p align="center"><img src=".github/hero.svg" alt="static" width="880"></p>

# Static

Static file server middleware for [Hanzo Ingress](https://github.com/hanzoai/ingress) (Traefik v3).

Serves static files, SPAs, directory listings, and custom error pages directly from the ingress layer — no backend required.

## Features

- Static file serving from any directory
- SPA mode (fallback to index for client-side routing)
- Custom index files per directory
- Directory listing with sortable HTML output
- Custom 404 error pages
- Cache-Control headers by file extension
- gzip on every text response (standalone binary), negotiated per request
- Hanzo tag injected into every document served
- Zero dependencies (pure Go stdlib)

## Compression

The standalone binary wraps its handler in `static.Compress`. Text goes out
gzipped when the client says it reads gzip; nothing else changes.

Measured on cloud.hanzo.ai's own export, 27 files, one cold load:
**1,586,676 B → 440,328 B, 72% off.** The document alone is 198,191 → 20,304
(89%), the stylesheet 146,300 → 20,701 (85%).

What it deliberately does not touch: already-compressed types (woff2, png,
mp4 — the allowlist is by Content-Type), bodies under 1 KB (a sub-MTU frame
costs the same either way and gzip's envelope can make it bigger), `Range`
requests (a range of the compressed stream is not the range that was asked
for), and `HEAD` (no body to compress, and its Content-Length must still
describe the resource). `Vary: Accept-Encoding` is set on every compressible
response, including the ones left identity, so shared caches key correctly.

There is no flag. A correct answer does not need to be opted into.

## Weight

~360 lines of Go. No external dependencies. Loaded via Yaegi interpreter at runtime — adds negligible overhead to ingress startup.

## Configuration

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `root` | String | `.` | Root directory to serve files from |
| `enableDirectoryListing` | Boolean | `false` | Enable directory browsing |
| `indexFiles` | Array | `["index.html", "index.htm"]` | Index filenames to try for directories |
| `spaMode` | Boolean | `false` | Redirect all 404s to SPA index |
| `spaIndex` | String | `index.html` | File to serve in SPA mode |
| `errorPage404` | String | `""` | Custom 404 page (relative to root) |
| `cacheControl` | Map | `{}` | File extension to Cache-Control value map |
| `key` | String | `""` | Site publishable key (`pk-…`). With one, every document served carries the Hanzo tag |

## Usage

### Embedded in Hanzo Ingress (local plugin)

Add to ingress static config:

```yaml
experimental:
  localPlugins:
    static:
      moduleName: github.com/hanzoai/static
```

Place the plugin source in `plugins-local/src/github.com/hanzoai/static/`.

Then use in dynamic config:

```yaml
http:
  routers:
    site:
      rule: Host(`example.com`)
      service: noop@internal
      middlewares:
        - static-files

  middlewares:
    static-files:
      plugin:
        static:
          root: /var/www/html
```

### Remote plugin

```yaml
experimental:
  plugins:
    static:
      moduleName: github.com/hanzoai/static
      version: v0.1.0
```

### Analytics

Give the server the site's publishable key and every HTML document it serves goes
out carrying the Hanzo tag:

```html
<script defer src="https://api.hanzo.ai/v1/event.js" data-key="pk-…"></script>
```

```yaml
http:
  middlewares:
    site:
      plugin:
        static:
          root: /var/www/html
          key: pk-…
```

Standalone: `--key=pk-…` or `HANZO_KEY=pk-…`.

The key belongs to the deployment, not to the bundle — the same build is servable
by any site, and a key that changes does not need a rebuild. Nothing on the
backend is ever modified; the tag exists only in the response.

Anything that is not a small HTML document with a `</head>` is served
byte-for-byte, and so is every document while no key is set. Where a
`Content-Security-Policy` is being sent, `https://api.hanzo.ai` is added to the
`script-src` and `connect-src` that need it — only when a tag was actually
injected, and only that one origin.

### SPA mode (React/Vue/Angular)

```yaml
http:
  middlewares:
    spa:
      plugin:
        static:
          root: /var/www/app
          spaMode: true
          spaIndex: index.html
          cacheControl:
            ".html": "no-cache"
            ".js": "max-age=31536000"
            ".css": "max-age=31536000"
            "*": "max-age=86400"
```

## Development

```bash
make lint    # golangci-lint
make test    # go test -v -cover
```

## License

Apache 2.0 — see [LICENSE](LICENSE).

Copyright 2025-2026 Hanzo AI Inc.
Based on [hhftechnology/statiq](https://github.com/hhftechnology/statiq) (Apache 2.0).
