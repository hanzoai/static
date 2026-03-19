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
- Zero dependencies (pure Go stdlib)

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
