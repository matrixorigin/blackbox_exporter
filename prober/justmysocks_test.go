// Copyright The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");

package prober

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/blackbox_exporter/config"
	"github.com/prometheus/client_golang/prometheus"
)

func TestDecodeJustMySocksCounter(t *testing.T) {
	counter, err := decodeJustMySocksCounter(strings.NewReader(`{
		"monthly_bw_limit_b": 5000000000000,
		"bw_counter_b": 711493882914
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if counter.monthlyLimitBytes != 5000000000000 || counter.usedBytes != 711493882914 {
		t.Fatalf("unexpected counter: %+v", counter)
	}
}

func TestDecodeJustMySocksCounterAcceptsStringsAndOverQuota(t *testing.T) {
	counter, err := decodeJustMySocksCounter(strings.NewReader(`{
		"monthly_bw_limit_b": "100",
		"bw_counter_b": "120"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := counter.usedBytes / counter.monthlyLimitBytes; got != 1.2 {
		t.Fatalf("usage ratio = %v, want 1.2", got)
	}
}

func TestDecodeJustMySocksCounterRejectsInvalidValues(t *testing.T) {
	for _, payload := range []string{
		`{"monthly_bw_limit_b": 0, "bw_counter_b": 1}`,
		`{"monthly_bw_limit_b": 100, "bw_counter_b": -1}`,
		`{"monthly_bw_limit_b": 100}`,
		`not-json`,
	} {
		if _, err := decodeJustMySocksCounter(strings.NewReader(payload)); err == nil {
			t.Fatalf("decodeJustMySocksCounter(%q) unexpectedly succeeded", payload)
		}
	}
}

func TestJustMySocksCacheRefreshesOnlyOncePerInterval(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"monthly_bw_limit_b":100,"bw_counter_b":90}`))
	}))
	defer server.Close()
	useJustMySocksTestClient(t, server.Client())

	urlFile := writeJustMySocksURLFile(t, server.URL)
	cache := &justMySocksCache{}
	cfg := config.JustMySocksProbe{URLFile: urlFile, RefreshInterval: time.Hour}
	now := time.Now()
	first := cache.refresh(context.Background(), cfg, now)
	second := cache.refresh(context.Background(), cfg, now.Add(time.Minute))
	if !first.lastAttemptSuccess || !second.lastAttemptSuccess {
		t.Fatal("expected successful cached probes")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
}

func TestJustMySocksCacheRetainsValuesAfterFailure(t *testing.T) {
	var fail atomic.Bool
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		if fail.Load() {
			http.Error(w, "failed", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"monthly_bw_limit_b":100,"bw_counter_b":120}`))
	}))
	defer server.Close()
	useJustMySocksTestClient(t, server.Client())

	urlFile := writeJustMySocksURLFile(t, server.URL)
	cache := &justMySocksCache{}
	cfg := config.JustMySocksProbe{URLFile: urlFile, RefreshInterval: time.Hour}
	now := time.Now()
	first := cache.refresh(context.Background(), cfg, now)
	if !first.lastAttemptSuccess {
		t.Fatal("first refresh failed")
	}
	fail.Store(true)
	second := cache.refresh(context.Background(), cfg, now.Add(2*time.Hour))
	if second.lastAttemptSuccess {
		t.Fatal("failed refresh reported success")
	}
	if !second.hasCounter || second.counter.usedBytes != 120 {
		t.Fatalf("last successful counter was not retained: %+v", second)
	}
	_ = cache.refresh(context.Background(), cfg, now.Add(2*time.Hour+4*time.Minute))
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests before failure retry = %d, want 2", got)
	}
	_ = cache.refresh(context.Background(), cfg, now.Add(2*time.Hour+6*time.Minute))
	if got := requests.Load(); got != 3 {
		t.Fatalf("requests after failure retry = %d, want 3", got)
	}

	registry := prometheus.NewPedanticRegistry()
	registerJustMySocksMetrics(registry, second)
	metrics, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	values := make(map[string]float64, len(metrics))
	for _, family := range metrics {
		if len(family.Metric) > 0 && family.Metric[0].Gauge != nil {
			values[family.GetName()] = family.Metric[0].Gauge.GetValue()
		}
	}
	if values["justmysocks_traffic_scrape_success"] != 0 {
		t.Fatalf("scrape success = %v, want 0", values["justmysocks_traffic_scrape_success"])
	}
	if values["justmysocks_traffic_usage_ratio"] != 1.2 {
		t.Fatalf("usage ratio = %v, want 1.2", values["justmysocks_traffic_usage_ratio"])
	}
	if values["justmysocks_traffic_remaining_bytes"] != 0 {
		t.Fatalf("remaining bytes = %v, want 0", values["justmysocks_traffic_remaining_bytes"])
	}
}

func TestFetchJustMySocksCounterDoesNotFollowRedirects(t *testing.T) {
	redirectFollowed := atomic.Bool{}
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectFollowed.Store(true)
	}))
	defer target.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()
	useJustMySocksTestClient(t, source.Client())

	_, err := fetchJustMySocksCounter(context.Background(), writeJustMySocksURLFile(t, source.URL+"?id=secret"))
	if err == nil || err.Error() != "traffic counter returned HTTP status 302" {
		t.Fatalf("unexpected error: %v", err)
	}
	if redirectFollowed.Load() {
		t.Fatal("credential-bearing redirect was followed")
	}
}

func TestFetchJustMySocksCounterRejectsPlainHTTP(t *testing.T) {
	_, err := fetchJustMySocksCounter(
		context.Background(),
		writeJustMySocksURLFile(t, "http://example.com/counter?id=secret"),
	)
	if err == nil || err.Error() != "traffic counter URL file is invalid" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func useJustMySocksTestClient(t *testing.T, client *http.Client) {
	t.Helper()
	previous := justMySocksHTTPClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	justMySocksHTTPClient = client
	t.Cleanup(func() {
		justMySocksHTTPClient = previous
	})
}

func writeJustMySocksURLFile(t *testing.T, endpoint string) string {
	t.Helper()
	path := t.TempDir() + "/counter-url"
	if err := os.WriteFile(path, []byte(endpoint+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
