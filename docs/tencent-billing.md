# Tencent Cloud account balance prober

The `tencent_billing` prober calls `DescribeAccountBalance` at the Tencent
Cloud China endpoint `https://billing.tencentcloudapi.com`. The `target`
parameter is a logical Prometheus target label only and is never used as an
API endpoint.

This prober collects the current account balance and collection health only.
It does not collect billing line items, product costs, instance costs, or
historical cost allocation data. Those require a separate billing-detail or
COS-based FinOps ingestion pipeline.

Configure a module:

```yaml
modules:
  tencent_cloud_account_balance:
    prober: tencent_billing
    timeout: 30s
    tencent_billing:
      refresh_interval: 1h
```

Provide credentials through environment variables, preferably from a
Kubernetes Secret:

```text
TENCENTCLOUD_SECRET_ID
TENCENTCLOUD_SECRET_KEY
```

Do not put credentials in `blackbox.yml`, a Probe target, or Prometheus
labels. The CAM identity only needs permission to call
`finance:DescribeAccountBalance`. The TC3 signing service name remains
`billing`.

The prober caches successful API responses in process memory. A normal probe
returns the cache until `refresh_interval` expires. After an API failure it
retains the last successful balance values, returns `probe_success 0`, and
retries after at most five minutes.

Balance metrics are expressed in Chinese yuan:

```text
tencentcloud_billing_real_balance_yuan
tencentcloud_billing_cash_account_balance_yuan
tencentcloud_billing_income_into_account_balance_yuan
tencentcloud_billing_present_account_balance_yuan
tencentcloud_billing_freeze_amount_yuan
tencentcloud_billing_owe_amount_yuan
tencentcloud_billing_credit_amount_yuan
tencentcloud_billing_credit_balance_yuan
tencentcloud_billing_real_credit_balance_yuan
```

Collection health metrics:

```text
probe_success
tencentcloud_billing_scrape_success
tencentcloud_billing_last_attempt_timestamp_seconds
tencentcloud_billing_last_success_timestamp_seconds
tencentcloud_billing_scrape_duration_seconds
```

The HTTP client honors `HTTP_PROXY`, `HTTPS_PROXY`, and `NO_PROXY`.
Redirects are deliberately not followed so the signed authorization request
cannot be forwarded or replayed against a different URL.
