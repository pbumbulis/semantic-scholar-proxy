package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestHealthCheck(t *testing.T) {
	h, err := newHandler("key", "http://unused", rate.NewLimiter(rate.Inf, 1))
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if body := rec.Body.String(); body != "ok" {
		t.Fatalf("want body %q, got %q", "ok", body)
	}
}

func TestAPIKeyInjected(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-api-key"); got != "test-key" {
			t.Errorf("want x-api-key %q, got %q", "test-key", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	h, err := newHandler("test-key", upstream.URL, rate.NewLimiter(rate.Inf, 1))
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/graph/v1/paper/search?query=test", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestAPIKeyOverwritesClientKey(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-api-key"); got != "server-key" {
			t.Errorf("want server-key, got %q", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	h, err := newHandler("server-key", upstream.URL, rate.NewLimiter(rate.Inf, 1))
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/graph/v1/paper/123", nil)
	req.Header.Set("x-api-key", "client-supplied-key") // must be overwritten

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestRequestForwardedToUpstream(t *testing.T) {
	var receivedPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	h, err := newHandler("k", upstream.URL, rate.NewLimiter(rate.Inf, 1))
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/graph/v1/paper/search", nil))

	if receivedPath != "/graph/v1/paper/search" {
		t.Errorf("want path /graph/v1/paper/search, got %q", receivedPath)
	}
}

func TestUpstreamErrorReturns502(t *testing.T) {
	// Point at a server that is immediately closed so the connection is refused.
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	gone.Close()

	h, err := newHandler("k", gone.URL, rate.NewLimiter(rate.Inf, 1))
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/any", nil))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", rec.Code)
	}
}

func TestRateLimiterBlocksSecondRequest(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	// Limiter with 1 token that refills once per hour — effectively blocks after the first request.
	limiter := rate.NewLimiter(rate.Every(time.Hour), 1)

	h, err := newHandler("k", upstream.URL, limiter)
	if err != nil {
		t.Fatal(err)
	}

	// First request consumes the single available token.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/first", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("first request: want 200, got %d", rec.Code)
	}

	// Second request must block; cancel it immediately via a pre-cancelled context.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the request so limiter.Wait returns immediately

	req := httptest.NewRequest(http.MethodGet, "/second", nil).WithContext(ctx)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("second request: want 503, got %d", rec.Code)
	}
}

func TestInvalidTargetURL(t *testing.T) {
	_, err := newHandler("k", "://bad url", rate.NewLimiter(rate.Inf, 1))
	if err == nil {
		t.Fatal("want error for invalid URL, got nil")
	}
}

// --- Retry-After / 429 handling ---

func TestParseRetryAfterSeconds(t *testing.T) {
	before := time.Now()
	got := parseRetryAfter("30")
	want := before.Add(30 * time.Second)
	if got.Before(want.Add(-time.Millisecond)) || got.After(want.Add(time.Millisecond)) {
		t.Errorf("parseRetryAfter(\"30\"): got %v, want ~%v", got, want)
	}
}

func TestParseRetryAfterHTTPDate(t *testing.T) {
	future := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	header := future.Format(http.TimeFormat)
	got := parseRetryAfter(header)
	if !got.Equal(future) {
		t.Errorf("parseRetryAfter(%q): got %v, want %v", header, got, future)
	}
}

func TestParseRetryAfterEmpty(t *testing.T) {
	before := time.Now()
	got := parseRetryAfter("")
	// Empty header falls back to a 1-second backoff.
	if got.Before(before) || got.After(before.Add(2*time.Second)) {
		t.Errorf("parseRetryAfter(\"\"): got %v, want ~1s from now", got)
	}
}

func TestBackoffBlocksAfter429(t *testing.T) {
	// Upstream returns 429 with a Retry-After far in the future.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(upstream.Close)

	h, err := newHandler("k", upstream.URL, rate.NewLimiter(rate.Inf, 1))
	if err != nil {
		t.Fatal(err)
	}

	// First request: receives and forwards the 429, sets the backoff.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/a", nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("first request: want 429, got %d", rec.Code)
	}

	// Second request: backoff is active; pre-cancelled context should return 503.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/b", nil).WithContext(ctx))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("second request: want 503 while in backoff, got %d", rec.Code)
	}
}

func TestBackoffClearsAfterExpiry(t *testing.T) {
	// Upstream: first call returns 429 with an already-elapsed Retry-After,
	// second call returns 200.
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			// Retry-After in the past — backoff window is already over.
			w.Header().Set("Retry-After", time.Now().Add(-time.Second).UTC().Format(http.TimeFormat))
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	h, err := newHandler("k", upstream.URL, rate.NewLimiter(rate.Inf, 1))
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/a", nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("first request: want 429, got %d", rec.Code)
	}

	// Backoff already expired — second request should proceed.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/b", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("second request: want 200 after expired backoff, got %d", rec.Code)
	}
}
