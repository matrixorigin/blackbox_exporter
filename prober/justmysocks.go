// Copyright The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package prober

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/blackbox_exporter/config"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	justMySocksMaxResponseBytes     = 1 << 20
	justMySocksFailureRetryInterval = 5 * time.Minute
)

var justMySocksHTTPClient = func() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyFromEnvironment
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}()

type justMySocksCounter struct {
	monthlyLimitBytes float64
	usedBytes         float64
}

type justMySocksSnapshot struct {
	counter            justMySocksCounter
	hasCounter         bool
	lastAttemptSuccess bool
	lastAttempt        time.Time
	lastSuccess        time.Time
	lastScrapeDuration time.Duration
	lastError          string
}

type justMySocksCache struct {
	mu          sync.Mutex
	snapshot    justMySocksSnapshot
	nextRefresh time.Time
}

var justMySocksCaches sync.Map

func ProbeJustMySocks(ctx context.Context, _ string, module config.Module, registry *prometheus.Registry, logger *slog.Logger) bool {
	cacheValue, _ := justMySocksCaches.LoadOrStore(module.JustMySocks.URLFile, &justMySocksCache{})
	cache := cacheValue.(*justMySocksCache)
	snapshot := cache.refresh(ctx, module.JustMySocks, time.Now())
	registerJustMySocksMetrics(registry, snapshot)
	if !snapshot.lastAttemptSuccess {
		logger.Error("Just My Socks traffic counter update failed", "err", snapshot.lastError)
	}
	return snapshot.lastAttemptSuccess
}

func (c *justMySocksCache) refresh(ctx context.Context, cfg config.JustMySocksProbe, now time.Time) justMySocksSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.nextRefresh.IsZero() && now.Before(c.nextRefresh) {
		return c.snapshot
	}

	startedAt := time.Now()
	counter, err := fetchJustMySocksCounter(ctx, cfg.URLFile)
	duration := time.Since(startedAt)
	finishedAt := now.Add(duration)
	c.snapshot.lastAttempt = finishedAt
	c.snapshot.lastScrapeDuration = duration
	if err != nil {
		c.snapshot.lastAttemptSuccess = false
		c.snapshot.lastError = err.Error()
		retryInterval := min(cfg.RefreshInterval, justMySocksFailureRetryInterval)
		c.nextRefresh = finishedAt.Add(retryInterval)
		return c.snapshot
	}

	c.snapshot.counter = counter
	c.snapshot.hasCounter = true
	c.snapshot.lastAttemptSuccess = true
	c.snapshot.lastSuccess = finishedAt
	c.snapshot.lastError = ""
	c.nextRefresh = finishedAt.Add(cfg.RefreshInterval)
	return c.snapshot
}

func fetchJustMySocksCounter(ctx context.Context, urlFile string) (justMySocksCounter, error) {
	endpointBytes, err := os.ReadFile(urlFile)
	if err != nil {
		return justMySocksCounter{}, errors.New("could not read traffic counter URL file")
	}
	endpoint := strings.TrimSpace(string(endpointBytes))
	parsedEndpoint, err := url.Parse(endpoint)
	if err != nil || parsedEndpoint.Scheme != "https" || parsedEndpoint.Host == "" {
		return justMySocksCounter{}, errors.New("traffic counter URL file is invalid")
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return justMySocksCounter{}, errors.New("could not create traffic counter request")
	}
	response, err := justMySocksHTTPClient.Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return justMySocksCounter{}, errors.New("traffic counter request timed out")
		}
		return justMySocksCounter{}, errors.New("traffic counter request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return justMySocksCounter{}, fmt.Errorf("traffic counter returned HTTP status %d", response.StatusCode)
	}
	return decodeJustMySocksCounter(io.LimitReader(response.Body, justMySocksMaxResponseBytes))
}

func decodeJustMySocksCounter(reader io.Reader) (justMySocksCounter, error) {
	var payload map[string]json.RawMessage
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(&payload); err != nil {
		return justMySocksCounter{}, errors.New("traffic counter returned invalid JSON")
	}
	monthlyLimit, err := decodeJustMySocksNonNegativeNumber(payload["monthly_bw_limit_b"])
	if err != nil || monthlyLimit <= 0 {
		return justMySocksCounter{}, errors.New("traffic counter has an invalid monthly_bw_limit_b")
	}
	used, err := decodeJustMySocksNonNegativeNumber(payload["bw_counter_b"])
	if err != nil {
		return justMySocksCounter{}, errors.New("traffic counter has an invalid bw_counter_b")
	}
	return justMySocksCounter{monthlyLimitBytes: monthlyLimit, usedBytes: used}, nil
}

func decodeJustMySocksNonNegativeNumber(raw json.RawMessage) (float64, error) {
	if len(raw) == 0 {
		return 0, errors.New("value is missing")
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return 0, err
	}
	var text string
	switch typed := value.(type) {
	case json.Number:
		text = typed.String()
	case string:
		text = typed
	default:
		return 0, errors.New("value is not numeric")
	}
	number, err := strconv.ParseFloat(text, 64)
	if err != nil || number < 0 || math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, errors.New("value is not a finite non-negative number")
	}
	return number, nil
}

func registerJustMySocksMetrics(registry *prometheus.Registry, snapshot justMySocksSnapshot) {
	registerGauge := func(name, help string, value float64) {
		metric := prometheus.NewGauge(prometheus.GaugeOpts{Name: name, Help: help})
		metric.Set(value)
		registry.MustRegister(metric)
	}

	success := 0.0
	if snapshot.lastAttemptSuccess {
		success = 1
	}
	registerGauge("justmysocks_traffic_scrape_success", "Whether the most recent Just My Socks traffic counter request succeeded.", success)
	if !snapshot.lastAttempt.IsZero() {
		registerGauge("justmysocks_traffic_last_attempt_timestamp_seconds", "Unix timestamp of the most recent Just My Socks traffic counter request.", float64(snapshot.lastAttempt.Unix()))
		registerGauge("justmysocks_traffic_scrape_duration_seconds", "Duration of the most recent Just My Socks traffic counter request in seconds.", snapshot.lastScrapeDuration.Seconds())
	}
	if !snapshot.lastSuccess.IsZero() {
		registerGauge("justmysocks_traffic_last_success_timestamp_seconds", "Unix timestamp of the most recent successful Just My Socks traffic counter request.", float64(snapshot.lastSuccess.Unix()))
	}
	if !snapshot.hasCounter {
		return
	}

	remaining := math.Max(snapshot.counter.monthlyLimitBytes-snapshot.counter.usedBytes, 0)
	usageRatio := snapshot.counter.usedBytes / snapshot.counter.monthlyLimitBytes
	registerGauge("justmysocks_traffic_monthly_quota_bytes", "Just My Socks monthly traffic quota in bytes.", snapshot.counter.monthlyLimitBytes)
	registerGauge("justmysocks_traffic_used_bytes", "Just My Socks traffic used in the current quota period in bytes.", snapshot.counter.usedBytes)
	registerGauge("justmysocks_traffic_remaining_bytes", "Just My Socks traffic remaining in the current quota period in bytes.", remaining)
	registerGauge("justmysocks_traffic_usage_ratio", "Just My Socks traffic usage ratio. A value of 1 means 100 percent of the monthly quota.", usageRatio)
}
