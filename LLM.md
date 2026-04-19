# static — AI Assistant Context

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

## Tests

```bash
go test ./cmd/static
```
