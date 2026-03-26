FROM golang:1.23-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /semantic-scholar-proxy .

# ---- final ----
# distroless/static ships CA certs, /etc/passwd (nonroot user), and nothing else.
# Equivalent to scratch + manual cert copy, but Google-maintained.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /semantic-scholar-proxy /semantic-scholar-proxy

EXPOSE 8080

ENTRYPOINT ["/semantic-scholar-proxy"]
