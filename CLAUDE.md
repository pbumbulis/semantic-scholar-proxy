# semantic-scholar-proxy

HTTP reverse proxy that rate-limits requests to the Semantic Scholar API and injects an API key. Runs in a homelab Kubernetes cluster; endpoint is exposed via Tailscale.

## Architecture

```
client → semantic-scholar-proxy (:8080) → https://api.semanticscholar.org
```

- **Rate limiting**: global token-bucket limiter, 1 req/s (burst 1). Callers block rather than receive 429s; cancelling the request context unblocks immediately.
- **API key**: injected via `x-api-key` header on every proxied request; any client-supplied value is overwritten.
- **Health check**: `GET /health` — returns 200 `ok`, used by Kubernetes liveness and readiness probes.
- **Graceful shutdown**: 15-second drain on SIGTERM/SIGINT.

## Configuration (environment variables)

| Variable | Required | Default | Description |
|---|---|---|---|
| `SEMANTIC_SCHOLAR_API_KEY` | yes | — | Semantic Scholar API key |
| `LISTEN_ADDR` | no | `:8080` | Address to listen on |
| `TARGET_URL` | no | `https://api.semanticscholar.org` | Upstream base URL |

## Conventions

- All logic lives in `main.go` — keep it that way unless the file exceeds ~200 lines.
- No external frameworks; stdlib + `golang.org/x/time/rate` only.
- The rate limiter is intentionally global (not per-client): all homelab traffic shares one API key and one upstream quota.
- The final image uses `gcr.io/distroless/static-debian12:nonroot`; do not switch to scratch or alpine.
