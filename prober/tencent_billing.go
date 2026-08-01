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
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
	"sync"
	"time"

	"github.com/prometheus/blackbox_exporter/config"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	tencentBillingEndpoint             = "https://billing.tencentcloudapi.com"
	tencentBillingService              = "billing"
	tencentBillingAction               = "DescribeAccountBalance"
	tencentBillingVersion              = "2018-07-09"
	tencentBillingContentType          = "application/json; charset=utf-8"
	tencentBillingSecretIDEnv          = "TENCENTCLOUD_SECRET_ID"
	tencentBillingSecretKeyEnv         = "TENCENTCLOUD_SECRET_KEY"
	tencentBillingMaxResponseBytes     = 1 << 20
	tencentBillingFailureRetryInterval = 5 * time.Minute
)

var (
	tencentBillingTransport = func() *http.Transport {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = http.ProxyFromEnvironment
		return transport
	}()
	tencentBillingHTTPClient = &http.Client{
		Transport: tencentBillingTransport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	tencentBillingCacheValue tencentBillingCache
)

type tencentBillingBalances struct {
	realBalance              float64
	cashAccountBalance       float64
	incomeIntoAccountBalance float64
	presentAccountBalance    float64
	freezeAmount             float64
	oweAmount                float64
	creditAmount             float64
	creditBalance            float64
	realCreditBalance        float64
}

type tencentBillingSnapshot struct {
	balances           tencentBillingBalances
	hasBalances        bool
	lastAttemptSuccess bool
	lastAttempt        time.Time
	lastSuccess        time.Time
	lastScrapeDuration time.Duration
	lastError          string
}

type tencentBillingCache struct {
	mu          sync.Mutex
	snapshot    tencentBillingSnapshot
	nextRefresh time.Time
}

type tencentBillingFetchFunc func(context.Context, time.Time) (tencentBillingBalances, error)

// ProbeTencentBilling reports the account balance for the Tencent Cloud
// credentials supplied through the process environment. target is deliberately
// ignored: it is only a logical target label in Prometheus.
func ProbeTencentBilling(ctx context.Context, _ string, module config.Module, registry *prometheus.Registry, logger *slog.Logger) bool {
	snapshot := tencentBillingCacheValue.refresh(
		ctx,
		module.TencentBilling,
		time.Now(),
		fetchTencentBillingBalancesFromEnvironment,
	)
	registerTencentBillingMetrics(registry, snapshot)
	if !snapshot.lastAttemptSuccess {
		logger.Error("Tencent Cloud account balance update failed", "err", snapshot.lastError)
	}
	return snapshot.lastAttemptSuccess
}

func (c *tencentBillingCache) refresh(
	ctx context.Context,
	cfg config.TencentBillingProbe,
	now time.Time,
	fetch tencentBillingFetchFunc,
) tencentBillingSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.nextRefresh.IsZero() && now.Before(c.nextRefresh) {
		return c.snapshot
	}

	startedAt := time.Now()
	balances, err := fetch(ctx, now)
	duration := time.Since(startedAt)
	finishedAt := now.Add(duration)

	c.snapshot.lastAttempt = finishedAt
	c.snapshot.lastScrapeDuration = duration
	if err != nil {
		c.snapshot.lastAttemptSuccess = false
		c.snapshot.lastError = err.Error()
		retryInterval := min(cfg.RefreshInterval, tencentBillingFailureRetryInterval)
		c.nextRefresh = finishedAt.Add(retryInterval)
		return c.snapshot
	}

	c.snapshot.balances = balances
	c.snapshot.hasBalances = true
	c.snapshot.lastAttemptSuccess = true
	c.snapshot.lastSuccess = finishedAt
	c.snapshot.lastError = ""
	c.nextRefresh = finishedAt.Add(cfg.RefreshInterval)
	return c.snapshot
}

func fetchTencentBillingBalancesFromEnvironment(ctx context.Context, now time.Time) (tencentBillingBalances, error) {
	secretID := os.Getenv(tencentBillingSecretIDEnv)
	secretKey := os.Getenv(tencentBillingSecretKeyEnv)
	if secretID == "" || secretKey == "" {
		return tencentBillingBalances{}, errors.New("Tencent Cloud billing credentials are not configured")
	}
	return fetchTencentBillingBalances(
		ctx,
		tencentBillingHTTPClient,
		tencentBillingEndpoint,
		secretID,
		secretKey,
		now.UTC(),
	)
}

type tencentBillingAPIResponse struct {
	Response struct {
		RealBalance              *float64         `json:"RealBalance"`
		CashAccountBalance       *float64         `json:"CashAccountBalance"`
		IncomeIntoAccountBalance *float64         `json:"IncomeIntoAccountBalance"`
		PresentAccountBalance    *float64         `json:"PresentAccountBalance"`
		FreezeAmount             *float64         `json:"FreezeAmount"`
		OweAmount                *float64         `json:"OweAmount"`
		CreditAmount             *float64         `json:"CreditAmount"`
		CreditBalance            *float64         `json:"CreditBalance"`
		RealCreditBalance        *float64         `json:"RealCreditBalance"`
		Error                    *tencentAPIError `json:"Error,omitempty"`
		RequestID                string           `json:"RequestId"`
	} `json:"Response"`
}

type tencentAPIError struct {
	Code string `json:"Code"`
}

func fetchTencentBillingBalances(
	ctx context.Context,
	client *http.Client,
	endpoint string,
	secretID string,
	secretKey string,
	now time.Time,
) (tencentBillingBalances, error) {
	payload := []byte("{}")
	endpointURL, err := url.Parse(endpoint)
	if err != nil || endpointURL.Scheme != "https" || endpointURL.Host == "" {
		return tencentBillingBalances{}, errors.New("Tencent Cloud billing endpoint is invalid")
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL.String(), bytes.NewReader(payload))
	if err != nil {
		return tencentBillingBalances{}, errors.New("could not create Tencent Cloud billing request")
	}
	request.Header.Set("Authorization", buildTencentBillingAuthorization(secretID, secretKey, endpointURL.Host, payload, now))
	request.Header.Set("Content-Type", tencentBillingContentType)
	request.Header.Set("X-TC-Action", tencentBillingAction)
	request.Header.Set("X-TC-Timestamp", strconv.FormatInt(now.Unix(), 10))
	request.Header.Set("X-TC-Version", tencentBillingVersion)
	request.Host = endpointURL.Host

	response, err := client.Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return tencentBillingBalances{}, errors.New("Tencent Cloud billing request timed out")
		}
		return tencentBillingBalances{}, errors.New("Tencent Cloud billing request failed")
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, tencentBillingMaxResponseBytes+1))
	if err != nil {
		return tencentBillingBalances{}, errors.New("could not read Tencent Cloud billing response")
	}
	if len(body) > tencentBillingMaxResponseBytes {
		return tencentBillingBalances{}, errors.New("Tencent Cloud billing response is too large")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return tencentBillingBalances{}, fmt.Errorf("Tencent Cloud billing returned HTTP status %d", response.StatusCode)
	}

	return decodeTencentBillingBalances(bytes.NewReader(body))
}

func decodeTencentBillingBalances(reader io.Reader) (tencentBillingBalances, error) {
	var payload tencentBillingAPIResponse
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(&payload); err != nil {
		return tencentBillingBalances{}, errors.New("Tencent Cloud billing returned invalid JSON")
	}
	if payload.Response.Error != nil {
		if payload.Response.Error.Code == "" {
			return tencentBillingBalances{}, errors.New("Tencent Cloud billing API returned an error")
		}
		return tencentBillingBalances{}, fmt.Errorf("Tencent Cloud billing API returned error %s", payload.Response.Error.Code)
	}
	if payload.Response.RequestID == "" {
		return tencentBillingBalances{}, errors.New("Tencent Cloud billing response is missing RequestId")
	}

	values := []struct {
		name  string
		value *float64
	}{
		{"RealBalance", payload.Response.RealBalance},
		{"CashAccountBalance", payload.Response.CashAccountBalance},
		{"IncomeIntoAccountBalance", payload.Response.IncomeIntoAccountBalance},
		{"PresentAccountBalance", payload.Response.PresentAccountBalance},
		{"FreezeAmount", payload.Response.FreezeAmount},
		{"OweAmount", payload.Response.OweAmount},
		{"CreditAmount", payload.Response.CreditAmount},
		{"CreditBalance", payload.Response.CreditBalance},
		{"RealCreditBalance", payload.Response.RealCreditBalance},
	}
	for _, value := range values {
		if value.value == nil {
			return tencentBillingBalances{}, fmt.Errorf("Tencent Cloud billing response is missing %s", value.name)
		}
		if math.IsNaN(*value.value) || math.IsInf(*value.value, 0) {
			return tencentBillingBalances{}, fmt.Errorf("Tencent Cloud billing response has invalid %s", value.name)
		}
	}

	return tencentBillingBalances{
		realBalance:              centsToYuan(*payload.Response.RealBalance),
		cashAccountBalance:       centsToYuan(*payload.Response.CashAccountBalance),
		incomeIntoAccountBalance: centsToYuan(*payload.Response.IncomeIntoAccountBalance),
		presentAccountBalance:    centsToYuan(*payload.Response.PresentAccountBalance),
		freezeAmount:             centsToYuan(*payload.Response.FreezeAmount),
		oweAmount:                centsToYuan(*payload.Response.OweAmount),
		creditAmount:             centsToYuan(*payload.Response.CreditAmount),
		creditBalance:            centsToYuan(*payload.Response.CreditBalance),
		realCreditBalance:        centsToYuan(*payload.Response.RealCreditBalance),
	}, nil
}

func buildTencentBillingAuthorization(secretID, secretKey, host string, payload []byte, now time.Time) string {
	date := now.UTC().Format("2006-01-02")
	timestamp := strconv.FormatInt(now.Unix(), 10)
	payloadHash := sha256Hex(payload)

	canonicalHeaders := "content-type:" + tencentBillingContentType + "\n" +
		"host:" + host + "\n" +
		"x-tc-action:" + lowerASCII(tencentBillingAction) + "\n"
	signedHeaders := "content-type;host;x-tc-action"
	canonicalRequest := http.MethodPost + "\n" +
		"/" + "\n" +
		"" + "\n" +
		canonicalHeaders + "\n" +
		signedHeaders + "\n" +
		payloadHash

	credentialScope := date + "/" + tencentBillingService + "/tc3_request"
	stringToSign := "TC3-HMAC-SHA256\n" +
		timestamp + "\n" +
		credentialScope + "\n" +
		sha256Hex([]byte(canonicalRequest))

	secretDate := hmacSHA256([]byte("TC3"+secretKey), date)
	secretService := hmacSHA256(secretDate, tencentBillingService)
	secretSigning := hmacSHA256(secretService, "tc3_request")
	signature := hex.EncodeToString(hmacSHA256(secretSigning, stringToSign))

	return "TC3-HMAC-SHA256 Credential=" + secretID + "/" + credentialScope +
		", SignedHeaders=" + signedHeaders +
		", Signature=" + signature
}

func registerTencentBillingMetrics(registry *prometheus.Registry, snapshot tencentBillingSnapshot) {
	registerGauge := func(name, help string, value float64) {
		metric := prometheus.NewGauge(prometheus.GaugeOpts{Name: name, Help: help})
		metric.Set(value)
		registry.MustRegister(metric)
	}

	success := 0.0
	if snapshot.lastAttemptSuccess {
		success = 1
	}
	registerGauge("tencentcloud_billing_scrape_success", "Whether the most recent Tencent Cloud account balance request succeeded.", success)
	registerGauge("tencentcloud_billing_last_attempt_timestamp_seconds", "Unix timestamp of the most recent Tencent Cloud account balance request.", timestampOrZero(snapshot.lastAttempt))
	registerGauge("tencentcloud_billing_last_success_timestamp_seconds", "Unix timestamp of the most recent successful Tencent Cloud account balance request.", timestampOrZero(snapshot.lastSuccess))
	registerGauge("tencentcloud_billing_scrape_duration_seconds", "Duration of the most recent Tencent Cloud account balance request in seconds.", snapshot.lastScrapeDuration.Seconds())

	if !snapshot.hasBalances {
		return
	}
	registerGauge("tencentcloud_billing_real_balance_yuan", "Tencent Cloud current real available balance in Chinese yuan.", snapshot.balances.realBalance)
	registerGauge("tencentcloud_billing_cash_account_balance_yuan", "Tencent Cloud cash account balance in Chinese yuan.", snapshot.balances.cashAccountBalance)
	registerGauge("tencentcloud_billing_income_into_account_balance_yuan", "Tencent Cloud income-transferred account balance in Chinese yuan.", snapshot.balances.incomeIntoAccountBalance)
	registerGauge("tencentcloud_billing_present_account_balance_yuan", "Tencent Cloud promotional account balance in Chinese yuan.", snapshot.balances.presentAccountBalance)
	registerGauge("tencentcloud_billing_freeze_amount_yuan", "Tencent Cloud frozen amount in Chinese yuan.", snapshot.balances.freezeAmount)
	registerGauge("tencentcloud_billing_owe_amount_yuan", "Tencent Cloud amount owed in Chinese yuan.", snapshot.balances.oweAmount)
	registerGauge("tencentcloud_billing_credit_amount_yuan", "Tencent Cloud credit limit in Chinese yuan.", snapshot.balances.creditAmount)
	registerGauge("tencentcloud_billing_credit_balance_yuan", "Tencent Cloud available credit balance in Chinese yuan.", snapshot.balances.creditBalance)
	registerGauge("tencentcloud_billing_real_credit_balance_yuan", "Tencent Cloud real available credit balance in Chinese yuan.", snapshot.balances.realCreditBalance)
}

func centsToYuan(value float64) float64 {
	return value / 100
}

func timestampOrZero(value time.Time) float64 {
	if value.IsZero() {
		return 0
	}
	return float64(value.Unix())
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, value string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(value))
	return mac.Sum(nil)
}

func lowerASCII(value string) string {
	output := []byte(value)
	for i, char := range output {
		if 'A' <= char && char <= 'Z' {
			output[i] = char + ('a' - 'A')
		}
	}
	return string(output)
}
