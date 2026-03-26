# semantic-scholar-proxy

HTTP reverse proxy that rate-limits requests to the Semantic Scholar API and injects an API key. Runs in a homelab Kubernetes cluster; endpoint is exposed via Tailscale.

## Architecture

```
client → semantic-scholar-proxy (:8080) → https://api.semanticscholar.org
```

- **Rate limiting**: global token-bucket limiter, 1 req/s (burst 1). Callers block rather than receive 429s; cancelling the request context unblocks immediately.
- **API key**: injected via `x-api-key` header on every proxied request.
- **Health check**: `GET /health` — returns 200 `ok`, used by Kubernetes probes.
- **Graceful shutdown**: 15-second drain on SIGTERM/SIGINT.

## Configuration (environment variables)

| Variable | Required | Default | Description |
|---|---|---|---|
| `SEMANTIC_SCHOLAR_API_KEY` | yes | — | Semantic Scholar API key |
| `LISTEN_ADDR` | no | `:8080` | Address to listen on |
| `TARGET_URL` | no | `https://api.semanticscholar.org` | Upstream base URL |

## Local development

```sh
export SEMANTIC_SCHOLAR_API_KEY=your_key_here
go run .
# proxy now listening on :8080
curl http://localhost:8080/graph/v1/paper/search?query=transformers
```

## Building the image

The Dockerfile uses a multi-stage build: `golang:1.23-alpine` builder → `scratch` final image. The image is ~10 MB; it includes only the binary and TLS root certificates.

Multi-arch build (matches CI):
```sh
docker buildx build --platform linux/amd64,linux/arm64 -t semantic-scholar-proxy:dev .
```

## CI/CD

GitHub Actions (`.github/workflows/build.yml`) builds and pushes to GHCR on every push to `main` and on version tags (`v*`). PRs build but do not push.

Image: `ghcr.io/pbumbulis/semantic-scholar-proxy`

Tags produced:
- `main` — latest commit on main branch
- `vX.Y.Z` / `vX.Y` — semver tags
- `sha-<short>` — per-commit SHA

## Kubernetes deployment

```sh
kubectl apply -f k8s/namespace.yaml

# Create the API key secret (do NOT commit real values):
kubectl create secret generic semantic-scholar-proxy \
  --namespace semantic-scholar \
  --from-literal=api-key=YOUR_KEY

kubectl apply -f k8s/deployment.yaml
kubectl apply -f k8s/service.yaml
```

The Service is `ClusterIP`; Tailscale's `tailscale-operator` or an `IngressClass` annotation exposes it outside the cluster.

## Conventions

- All logic lives in `main.go` — keep it that way unless the file exceeds ~200 lines.
- No external frameworks; stdlib + `golang.org/x/time/rate` only.
- Version tags follow semver: `git tag v1.2.3 && git push --tags`.
