# static

Static file server for Hanzo Ingress (Traefik v3) and standalone SPA runtime.

## Two uses

1. **Yaegi plugin** embedded in `hanzoai/ingress` (`statiq.go`) — serves files at the edge.
2. **Standalone binary** (`cmd/static`, FROM scratch image) — base image for SPAs. Serves `/public` and templates `/public/config.json` from `SPA_*` env vars on startup.

## Runtime config (standalone binary)

The entrypoint writes `config.json` before serving. Env vars prefixed `SPA_` become camelCase keys:

| Env var | Key in config.json | Type |
|---------|--------------------|------|
| `SPA_ENV` | `env` | string |
| `SPA_API_HOST` | `apiHost` | string |
| `SPA_IAM_HOST` | `iamHost` | string |
| `SPA_RPC_HOST` | `rpcHost` | string |
| `SPA_ID_HOST` | `idHost` | string |
| `SPA_CHAIN_ID` | `chainId` | number (auto-detect all-digits) |
| `SPA_FEATURE_*` | `feature*` | bool (auto-detect true/false) or string |

If no `SPA_*` vars are set the placeholder `config.json` in the image is left untouched — the SPA falls back to its own defaults.

`v` (schema version) is always `1`. SPAs read `/config.json` at boot and validate.

Why at startup: deterministic ordering, one shot, no sidecar, no shell, no race with the HTTP server. Works in `FROM scratch` because the writer is Go.

## The tag

Every HTML document the server sends carries the Hanzo tag, stamped with the
site's publishable key (`key` in the plugin config; `--key` / `HANZO_KEY` for the
standalone binary). No key, no tag. `tag.go`.

The key lives in the DEPLOYMENT, not in the bundle. Baking it at build time is
what let the `hanzo.ai` apex serve a copy carrying no key at all, and `www`
0.1.15 serve one whose project had been deleted — a rebuild was the only way to
fix either.

Fail-safe by construction: only `text/html` under 1 MB with a `</head>` is
touched, the bytes are returned unchanged on every other path, a key that is not
`pk-` or is not attribute-safe injects nothing, and the file on the backend is
never modified.

`admit` adds `https://api.hanzo.ai` to the `script-src`/`connect-src` of the
policy the response is already carrying, and only when a tag went in. Without it
the tag is inert on 22 of the 23 sites this image serves: their CSPs name
api.hanzo.ai in `connect-src` for `/v1/event` but not in `script-src`, so the
script never loads.

## Tests

```bash
go test ./cmd/static
```
