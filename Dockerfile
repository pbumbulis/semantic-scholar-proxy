FROM golang:1.23-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /semantic-scholar-proxy .

# ---- final ----
FROM scratch

# TLS root certificates needed to reach api.semanticscholar.org (HTTPS).
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

COPY --from=builder /semantic-scholar-proxy /semantic-scholar-proxy

EXPOSE 8080

ENTRYPOINT ["/semantic-scholar-proxy"]
