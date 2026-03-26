package main

import (
	"context"
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

func main() {
	apiKey := mustEnv("SEMANTIC_SCHOLAR_API_KEY")
	listenAddr := envOr("LISTEN_ADDR", ":8080")
	targetRaw := envOr("TARGET_URL", "https://api.semanticscholar.org")

	// 1 request per second, burst of 1 — matches Semantic Scholar's rate limit.
	limiter := rate.NewLimiter(rate.Every(time.Second), 1)

	handler, err := newHandler(apiKey, targetRaw, limiter)
	if err != nil {
		slog.Error("failed to build handler", "err", err)
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
			slog.Error("server error", "err", err)
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
		slog.Error("shutdown error", "err", err)
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
				slog.Warn("upstream rate limited", "retry-after", resp.Header.Get("Retry-After"), "backoff-until", until)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Error("proxy error", "err", err, "path", r.URL.Path)
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

	return mux, nil
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
