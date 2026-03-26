package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/time/rate"
)

func main() {
	apiKey := mustEnv("SEMANTIC_SCHOLAR_API_KEY")
	listenAddr := envOr("LISTEN_ADDR", ":8080")
	targetRaw := envOr("TARGET_URL", "https://api.semanticscholar.org")

	target, err := url.Parse(targetRaw)
	if err != nil {
		slog.Error("invalid TARGET_URL", "err", err)
		os.Exit(1)
	}

	// 1 request per second, burst of 1 — matches Semantic Scholar's unauthenticated limit.
	// With a valid API key the limit is higher, but we stay conservative.
	limiter := rate.NewLimiter(rate.Every(time.Second), 1)

	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = target.Host
			pr.Out.Header.Set("x-api-key", apiKey)
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
			// Context cancelled (client disconnected) or deadline exceeded.
			http.Error(w, "request cancelled", http.StatusServiceUnavailable)
			return
		}
		proxy.ServeHTTP(w, r)
	})

	srv := &http.Server{
		Addr:         listenAddr,
		Handler:      mux,
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
