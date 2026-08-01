# Just My Socks traffic prober

This fork adds a `justmysocks` prober to the upstream Blackbox Exporter
multi-target exporter. The extension keeps all upstream HTTP, TCP, ICMP, DNS,
and gRPC probers intact.

The traffic-counter URL contains credentials and must not be supplied through
the public `target` query parameter. Mount it as a file from a Kubernetes
Secret instead. The URL must use HTTPS:

```yaml
modules:
  shanghai_vpn_traffic:
    prober: justmysocks
    timeout: 30s
    justmysocks:
      url_file: /etc/blackbox-secrets/justmysocks-url
      refresh_interval: 1h
```

Prometheus can then probe a non-sensitive alias:

```text
/probe?module=shanghai_vpn_traffic&target=shanghai-vpn
```

The prober reads `monthly_bw_limit_b` as the monthly quota and `bw_counter_b`
as the used traffic. It exposes:

- `probe_success`
- `justmysocks_traffic_monthly_quota_bytes`
- `justmysocks_traffic_used_bytes`
- `justmysocks_traffic_remaining_bytes`
- `justmysocks_traffic_usage_ratio`
- `justmysocks_traffic_scrape_success`
- last-attempt, last-success, and request-duration metrics

Results are cached for `refresh_interval`. A failed refresh retains the last
successful quota values while setting both `probe_success` and
`justmysocks_traffic_scrape_success` to `0`, and is retried after at most five
minutes. Redirects are deliberately not followed so the credential-bearing
URL cannot be forwarded to another host.
