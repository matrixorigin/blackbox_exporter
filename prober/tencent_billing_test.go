// Copyright The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");

package prober

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/blackbox_exporter/config"
	"github.com/prometheus/client_golang/prometheus"
)

const successfulTencentBillingResponse = `{
  "Response": {
    "RealBalance": 12345,
    "CashAccountBalance": 10000,
    "IncomeIntoAccountBalance": 2000,
    "PresentAccountBalance": 500,
    "FreezeAmount": 100,
    "OweAmount": 55,
    "CreditAmount": 100000,
    "CreditBalance": 112445,
    "RealCreditBalance": 112345,
    "RequestId": "test-request-id"
  }
}`

func TestBuildTencentBillingAuthorization(t *testing.T) {
	now := time.Unix(1551113065, 0).UTC()
	got := buildTencentBillingAuthorization(
		"AKIDEXAMPLE",
		"SECRETKEYEXAMPLE",
		"billing.tencentcloudapi.com",
		[]byte("{}"),
		now,
	)
	const want = "TC3-HMAC-SHA256 Credential=AKIDEXAMPLE/2019-02-25/billing/tc3_request, SignedHeaders=content-type;host;x-tc-action, Signature=a84b880c00ab3fb7c34193320eaf1f0922356e471e3296298372c8543ec9f3ca"
	if got != want {
		t.Fatalf("authorization = %q, want %q", got, want)
	}
}

func TestFetchTencentBillingBalances(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", request.Method)
		}
		if request.URL.Path != "/" {
			t.Errorf("path = %q, want /", request.URL.Path)
		}
		if request.Header.Get("X-TC-Action") != tencentBillingAction {
			t.Errorf("X-TC-Action = %q", request.Header.Get("X-TC-Action"))
		}
		if request.Header.Get("X-TC-Version") != tencentBillingVersion {
			t.Errorf("X-TC-Version = %q", request.Header.Get("X-TC-Version"))
		}
		if request.Header.Get("X-TC-Timestamp") != "1700000000" {
			t.Errorf("X-TC-Timestamp = %q", request.Header.Get("X-TC-Timestamp"))
		}
		wantAuthorization := buildTencentBillingAuthorization("test-id", "test-key", request.Host, []byte("{}"), now)
		if request.Header.Get("Authorization") != wantAuthorization {
			t.Error("request Authorization header does not have the expected TC3 signature")
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
		}
		if string(body) != "{}" {
			t.Errorf("body = %q, want {}", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, successfulTencentBillingResponse)
	}))
	defer server.Close()

	balances, err := fetchTencentBillingBalances(
		context.Background(),
		server.Client(),
		server.URL,
		"test-id",
		"test-key",
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if balances.realBalance != 123.45 {
		t.Errorf("realBalance = %v, want 123.45", balances.realBalance)
	}
	if balances.cashAccountBalance != 100 {
		t.Errorf("cashAccountBalance = %v, want 100", balances.cashAccountBalance)
	}
	if balances.incomeIntoAccountBalance != 20 {
		t.Errorf("incomeIntoAccountBalance = %v, want 20", balances.incomeIntoAccountBalance)
	}
	if balances.presentAccountBalance != 5 {
		t.Errorf("presentAccountBalance = %v, want 5", balances.presentAccountBalance)
	}
	if balances.freezeAmount != 1 {
		t.Errorf("freezeAmount = %v, want 1", balances.freezeAmount)
	}
	if balances.oweAmount != 0.55 {
		t.Errorf("oweAmount = %v, want 0.55", balances.oweAmount)
	}
	if balances.creditAmount != 1000 {
		t.Errorf("creditAmount = %v, want 1000", balances.creditAmount)
	}
	if balances.creditBalance != 1124.45 {
		t.Errorf("creditBalance = %v, want 1124.45", balances.creditBalance)
	}
	if balances.realCreditBalance != 1123.45 {
		t.Errorf("realCreditBalance = %v, want 1123.45", balances.realCreditBalance)
	}
}

func TestFetchTencentBillingBalancesDoesNotFollowRedirects(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusTemporaryRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var redirectFollowed atomic.Bool
			target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				redirectFollowed.Store(true)
			}))
			defer target.Close()
			source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL, status)
			}))
			defer source.Close()

			client := source.Client()
			client.CheckRedirect = tencentBillingHTTPClient.CheckRedirect
			_, err := fetchTencentBillingBalances(
				context.Background(),
				client,
				source.URL,
				"test-id",
				"test-key",
				time.Now(),
			)
			wantError := fmt.Sprintf("Tencent Cloud billing returned HTTP status %d", status)
			if err == nil || err.Error() != wantError {
				t.Fatalf("error = %v, want %q", err, wantError)
			}
			if redirectFollowed.Load() {
				t.Fatal("signed Tencent Cloud billing redirect was followed")
			}
		})
	}
}

func TestDecodeTencentBillingBalancesRejectsInvalidResponses(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		error   string
	}{
		{
			name:    "invalid JSON",
			payload: `{`,
			error:   "Tencent Cloud billing returned invalid JSON",
		},
		{
			name:    "API error",
			payload: `{"Response":{"Error":{"Code":"UnauthorizedOperation.CamNoAuth"},"RequestId":"request-id"}}`,
			error:   "Tencent Cloud billing API returned error UnauthorizedOperation.CamNoAuth",
		},
		{
			name:    "missing request ID",
			payload: strings.Replace(successfulTencentBillingResponse, `"RequestId": "test-request-id"`, `"RequestId": ""`, 1),
			error:   "Tencent Cloud billing response is missing RequestId",
		},
		{
			name:    "missing amount",
			payload: strings.Replace(successfulTencentBillingResponse, `"RealBalance": 12345,`, "", 1),
			error:   "Tencent Cloud billing response is missing RealBalance",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeTencentBillingBalances(strings.NewReader(test.payload))
			if err == nil || err.Error() != test.error {
				t.Fatalf("error = %v, want %q", err, test.error)
			}
		})
	}
}

func TestTencentBillingCacheRefreshesOnlyOncePerInterval(t *testing.T) {
	var requests atomic.Int32
	fetch := func(context.Context, time.Time) (tencentBillingBalances, error) {
		requests.Add(1)
		return tencentBillingBalances{realBalance: 10}, nil
	}
	cache := &tencentBillingCache{}
	cfg := config.TencentBillingProbe{RefreshInterval: time.Hour}
	now := time.Now()

	first := cache.refresh(context.Background(), cfg, now, fetch)
	second := cache.refresh(context.Background(), cfg, now.Add(time.Minute), fetch)
	if !first.lastAttemptSuccess || !second.lastAttemptSuccess {
		t.Fatal("expected successful cached probes")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
}

func TestTencentBillingCacheRetainsValuesAndRetriesSoonerAfterFailure(t *testing.T) {
	var requests atomic.Int32
	fail := atomic.Bool{}
	fetch := func(context.Context, time.Time) (tencentBillingBalances, error) {
		requests.Add(1)
		if fail.Load() {
			return tencentBillingBalances{}, fmt.Errorf("request failed")
		}
		return tencentBillingBalances{
			realBalance:              123.45,
			cashAccountBalance:       100,
			incomeIntoAccountBalance: 20,
			presentAccountBalance:    5,
			freezeAmount:             1,
			oweAmount:                1.25,
			creditAmount:             1000,
			creditBalance:            1124.45,
			realCreditBalance:        456.78,
		}, nil
	}
	cache := &tencentBillingCache{}
	cfg := config.TencentBillingProbe{RefreshInterval: time.Hour}
	now := time.Now()

	first := cache.refresh(context.Background(), cfg, now, fetch)
	if !first.lastAttemptSuccess {
		t.Fatal("first refresh failed")
	}

	fail.Store(true)
	second := cache.refresh(context.Background(), cfg, now.Add(2*time.Hour), fetch)
	if second.lastAttemptSuccess {
		t.Fatal("failed refresh reported success")
	}
	if !second.hasBalances || second.balances.realBalance != 123.45 {
		t.Fatalf("last successful balances were not retained: %+v", second)
	}
	if second.lastSuccess != first.lastSuccess {
		t.Fatalf("last success changed after failure: first=%s second=%s", first.lastSuccess, second.lastSuccess)
	}

	_ = cache.refresh(context.Background(), cfg, now.Add(2*time.Hour+4*time.Minute), fetch)
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests before failure retry = %d, want 2", got)
	}
	_ = cache.refresh(context.Background(), cfg, now.Add(2*time.Hour+6*time.Minute), fetch)
	if got := requests.Load(); got != 3 {
		t.Fatalf("requests after failure retry = %d, want 3", got)
	}

	registry := prometheus.NewPedanticRegistry()
	registerTencentBillingMetrics(registry, second)
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
	if values["tencentcloud_billing_scrape_success"] != 0 {
		t.Errorf("scrape success = %v, want 0", values["tencentcloud_billing_scrape_success"])
	}
	expectedBalances := map[string]float64{
		"tencentcloud_billing_real_balance_yuan":                123.45,
		"tencentcloud_billing_cash_account_balance_yuan":        100,
		"tencentcloud_billing_income_into_account_balance_yuan": 20,
		"tencentcloud_billing_present_account_balance_yuan":     5,
		"tencentcloud_billing_freeze_amount_yuan":               1,
		"tencentcloud_billing_owe_amount_yuan":                  1.25,
		"tencentcloud_billing_credit_amount_yuan":               1000,
		"tencentcloud_billing_credit_balance_yuan":              1124.45,
		"tencentcloud_billing_real_credit_balance_yuan":         456.78,
	}
	for name, want := range expectedBalances {
		if got := values[name]; got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
}

func TestFetchTencentBillingBalancesRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", tencentBillingMaxResponseBytes+1))
	}))
	defer server.Close()

	_, err := fetchTencentBillingBalances(
		context.Background(),
		server.Client(),
		server.URL,
		"test-id",
		"test-key",
		time.Now(),
	)
	if err == nil || err.Error() != "Tencent Cloud billing response is too large" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFetchTencentBillingBalancesFromEnvironmentRequiresBothCredentials(t *testing.T) {
	t.Setenv(tencentBillingSecretIDEnv, "")
	t.Setenv(tencentBillingSecretKeyEnv, "")
	_, err := fetchTencentBillingBalancesFromEnvironment(context.Background(), time.Now())
	if err == nil || err.Error() != "Tencent Cloud billing credentials are not configured" {
		t.Fatalf("unexpected error: %v", err)
	}
}
