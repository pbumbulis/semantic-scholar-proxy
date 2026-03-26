# semantic-scholar-proxy

HTTP reverse proxy for the [Semantic Scholar API](https://api.semanticscholar.org) that enforces the 1 req/s rate limit and injects an API key. Intended for homelab use: runs in Kubernetes, exposed via Tailscale.

```
client → semantic-scholar-proxy (:8080) → https://api.semanticscholar.org
```

- **Rate limiting**: global token-bucket, 1 req/s (burst 1). Clients block rather than receive 429s.
- **API key injection**: `x-api-key` header set on every upstream request; client-supplied values are overwritten.
- **Health check**: `GET /health` → `200 ok`
- **Graceful shutdown**: 15-second drain on SIGTERM/SIGINT.

## Configuration

| Variable | Required | Default | Description |
|---|---|---|---|
| `SEMANTIC_SCHOLAR_API_KEY` | yes | — | Semantic Scholar API key |
| `LISTEN_ADDR` | no | `:8080` | Address to listen on |
| `TARGET_URL` | no | `https://api.semanticscholar.org` | Upstream base URL |

## Local development

```sh
export SEMANTIC_SCHOLAR_API_KEY=your_key_here
go run .
curl http://localhost:8080/graph/v1/paper/search?query=transformers
```

## Container image

Multi-stage build: `golang:1.23-alpine` builder → `gcr.io/distroless/static-debian12:nonroot` final. The final image is ~2 MB and contains only the binary; CA certificates and a non-root user are provided by the distroless base.

Build locally (matches CI platforms):

```sh
docker buildx build --platform linux/amd64,linux/arm64 -t semantic-scholar-proxy:dev .
```

## CI/CD

GitHub Actions builds on every push to `main` and on `v*` tags, pushing to GHCR:

```
ghcr.io/pbumbulis/semantic-scholar-proxy
```

| Tag pattern | When |
|---|---|
| `main` | Every push to main |
| `vX.Y.Z`, `vX.Y` | Semver release tags |
| `sha-<short>` | Every push (immutable reference) |

To cut a release: `git tag v1.2.3 && git push --tags`

## Kubernetes deployment

```sh
kubectl apply -f k8s/namespace.yaml

# Create the secret — do NOT commit real values
kubectl create secret generic semantic-scholar-proxy \
  --namespace semantic-scholar \
  --from-literal=api-key=YOUR_KEY

kubectl apply -f k8s/deployment.yaml
kubectl apply -f k8s/service.yaml
```

The Service is `ClusterIP`. Expose it via the [Tailscale operator](https://tailscale.com/kb/1185/kubernetes) by annotating the Service or adding an `Ingress` with `ingressClassName: tailscale`.
