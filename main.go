package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/time/rate"
)

type contextKey int

const requestIDKey contextKey = 0

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "semantic-scholar-proxy")
	slog.SetDefault(logger)

	apiKey := mustEnv("SEMANTIC_SCHOLAR_API_KEY")
	listenAddr := envOr("LISTEN_ADDR", ":8080")
	targetRaw := envOr("TARGET_URL", "https://api.semanticscholar.org")

	// 1 request per second, burst of 1 — matches Semantic Scholar's rate limit.
	limiter := rate.NewLimiter(rate.Every(time.Second), 1)

	handler, err := newHandler(apiKey, targetRaw, limiter)
	if err != nil {
		slog.Error("failed to build handler", slog.Group("error", "type", fmt.Sprintf("%T", err), "message", err.Error()))
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:         listenAddr,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		slog.Info("listening", "addr", listenAddr, "target", targetRaw)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", slog.Group("error", "type", fmt.Sprintf("%T", err), "message", err.Error()))
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	slog.Info("shutting down")
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("shutdown error", slog.Group("error", "type", fmt.Sprintf("%T", err), "message", err.Error()))
	}
}

// newHandler builds the HTTP handler. Extracted for testability.
func newHandler(apiKey, targetRaw string, limiter *rate.Limiter) (http.Handler, error) {
	target, err := url.Parse(targetRaw)
	if err != nil {
		return nil, fmt.Errorf("invalid target URL: %w", err)
	}

	var backoffUntil atomic.Pointer[time.Time]

	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = target.Host
			pr.Out.Header.Set("x-api-key", apiKey)
		},
		ModifyResponse: func(resp *http.Response) error {
			if resp.StatusCode == http.StatusTooManyRequests {
				until := parseRetryAfter(resp.Header.Get("Retry-After"))
				backoffUntil.Store(&until)
				slog.Warn("upstream rate limited",
					"retry_after", resp.Header.Get("Retry-After"),
					"backoff_until", until,
					"request_id", requestIDFromCtx(resp.Request.Context()),
				)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Error("proxy error",
				slog.Group("error", "type", fmt.Sprintf("%T", err), "message", err.Error()),
				"path", r.URL.Path,
				"request_id", requestIDFromCtx(r.Context()),
			)
			http.Error(w, "bad gateway", http.StatusBadGateway)
		},
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if err := limiter.Wait(r.Context()); err != nil {
			http.Error(w, "request cancelled", http.StatusServiceUnavailable)
			return
		}
		if until := backoffUntil.Load(); until != nil {
			if delay := time.Until(*until); delay > 0 {
				select {
				case <-time.After(delay):
				case <-r.Context().Done():
					http.Error(w, "request cancelled", http.StatusServiceUnavailable)
					return
				}
			}
		}
		proxy.ServeHTTP(w, r)
	})

	return withRequestID(mux), nil
}

// withRequestID generates a unique ID per request and stores it in the context.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b [8]byte
		_, _ = rand.Read(b[:])
		id := fmt.Sprintf("%x", b)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

func requestIDFromCtx(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey).(string); ok {
		return id
	}
	return ""
}

// parseRetryAfter converts a Retry-After header value to an absolute time.
// Handles both integer seconds and HTTP-date formats per RFC 9110 §10.2.3.
// Falls back to a 1-second backoff if the header is absent or unparseable.
func parseRetryAfter(v string) time.Time {
	v = strings.TrimSpace(v)
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Now().Add(time.Duration(secs) * time.Second)
	}
	if t, err := http.ParseTime(v); err == nil {
		return t
	}
	return time.Now().Add(time.Second)
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		slog.Error("required environment variable not set", "key", key)
		os.Exit(1)
	}
	return v
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
