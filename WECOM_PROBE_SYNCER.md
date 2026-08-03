# WeCom Probe Syncer

`wecom_probe_syncer` reads probe targets from a WeCom smart sheet and reconciles
them into Prometheus Operator `Probe` resources. Prometheus then scrapes the
existing Blackbox exporter through those `Probe` resources.

## Data Flow

```text
WeCom smart sheet
  -> wecom_probe_syncer
  -> monitoring.coreos.com/v1 Probe
  -> Prometheus Operator
  -> Prometheus
  -> blackbox_exporter /probe
```

The syncer does not perform probes itself. It only manages `Probe` configuration.

## Smart Sheet Columns

Each row may contain extra business fields. The syncer reads only the configured
columns and ignores everything else.

The core columns are:

| Column | Required | Example |
| --- | --- | --- |
| `enabled` | no | `true` |
| `module` | no | `http_2xx` |
| `target` | yes | `https://matrixorigin.cn/` |

`enabled` is case-insensitive. A row is synced only when this column is
explicitly enabled, such as `true`, `yes`, `1`, `enabled`, `enable`, `启用`, or
`是`. Empty or unrecognized values are treated as disabled.

If optional columns exist, the syncer can read them as overrides. If they are
missing or empty, defaults are used:

| Optional column | Default |
| --- | --- |
| `name` | generated from `target` |
| `interval` | `30s` |
| `scrape_timeout` | `10s` |
| `job_name` | generated from the Probe name |
| `labels` | no extra labels |

Extra label columns can be configured with `columns.label_columns`.

One document can contain multiple smart sheets. Configure `wecom.sheet_ids` to
sync all of them with one syncer. The legacy single `wecom.sheet_id` field is
still supported. When multiple sheets are configured, generated Probe resource
names include the source sheet ID, so identical `name` values in different
sheets do not collide.

Example rows:

| enabled | module | target |
| --- | --- | --- |
| `true` | `http_2xx` | `https://matrixorigin.cn/` |
| `true` | `tcp_connect` | `shanghai.idc.matrixorigin.cn:30009` |
| `true` | `icmp` | `shanghai.idc.matrixorigin.cn` |
| `true` | `ssh_banner` | `shanghai.idc.matrixorigin.cn:22` |

## Configuration

See `wecom-probe-syncer.example.yml`.

For the current `mo-ob` cluster, the important settings are:

```yaml
kubernetes:
  namespace: mo-ob
  prober_url: ai-vllm-blackbox-exporter.mo-ob.svc:9115
  release_label: mo-ob-opensource-tke
```

The `release_label` is required because the cluster Prometheus selects `Probe`
resources with:

```yaml
probeSelector:
  matchLabels:
    release: mo-ob-opensource-tke
```

## Running Locally

```bash
rtk go run ./cmd/wecom_probe_syncer \
  --config.file=wecom-probe-syncer.example.yml \
  --once \
  --dry-run
```

For local Kubernetes writes, set `KUBECONFIG`:

```bash
rtk env KUBECONFIG=/path/to/kubeconfig \
  go run ./cmd/wecom_probe_syncer --config.file=wecom-probe-syncer.yml --once
```

## Kubernetes RBAC

The syncer needs permissions only for Prometheus Operator `Probe` resources.

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: wecom-probe-syncer
  namespace: mo-ob
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: wecom-probe-syncer
  namespace: mo-ob
rules:
  - apiGroups: ["monitoring.coreos.com"]
    resources: ["probes"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: wecom-probe-syncer
  namespace: mo-ob
subjects:
  - kind: ServiceAccount
    name: wecom-probe-syncer
    namespace: mo-ob
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: wecom-probe-syncer
```

## Kubernetes Deployment

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: wecom-probe-syncer
  namespace: mo-ob
type: Opaque
stringData:
  corpid: replace-me
  corpsecret: replace-me
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: wecom-probe-syncer
  namespace: mo-ob
data:
  config.yml: |
    sync_interval: 1m
    wecom:
      corpid_file: /etc/wecom-probe-syncer/secrets/corpid
      corpsecret_file: /etc/wecom-probe-syncer/secrets/corpsecret
      docid: replace-with-docid
      sheet_ids:
        - replace-with-sheet-id
    kubernetes:
      namespace: mo-ob
      prober_url: ai-vllm-blackbox-exporter.mo-ob.svc:9115
      release_label: mo-ob-opensource-tke
      prune: true
    columns:
      enabled: enabled
      module: module
      target: target
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: wecom-probe-syncer
  namespace: mo-ob
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: wecom-probe-syncer
  template:
    metadata:
      labels:
        app.kubernetes.io/name: wecom-probe-syncer
    spec:
      serviceAccountName: wecom-probe-syncer
      containers:
        - name: syncer
          image: replace-with-registry/wecom-probe-syncer:latest
          args:
            - --config.file=/etc/wecom-probe-syncer/config/config.yml
          volumeMounts:
            - name: config
              mountPath: /etc/wecom-probe-syncer/config
              readOnly: true
            - name: secrets
              mountPath: /etc/wecom-probe-syncer/secrets
              readOnly: true
      volumes:
        - name: config
          configMap:
            name: wecom-probe-syncer
        - name: secrets
          secret:
            secretName: wecom-probe-syncer
```

## Blackbox Modules

The current `mo-ob` Blackbox exporter ConfigMap already has `http_2xx`,
`http_2xx_3xx_no_redirect`, and `tcp_connect`. To use `icmp` and `ssh_banner`
from the smart sheet, add those modules to the Blackbox exporter configuration
and ensure ICMP has the required container network capability if the runtime
needs raw sockets.
