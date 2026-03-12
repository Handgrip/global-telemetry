# Global Probe Agent

Minimal distributed network monitoring system. A single Go binary per node that runs periodic ICMP and HTTP/HTTPS probes, then pushes results to Grafana Cloud via Prometheus Remote Write.

## Architecture

```
┌────────────────────┐
│  targets.json      │  (S3 / GitHub Raw / any HTTP server)
│  (remote config)   │
└────────┬───────────┘
         │ HTTP GET (periodic)
         ▼
┌────────────────────┐         Prometheus Remote Write
│   probe-agent      │ ──────────────────────────────────► Grafana Cloud
│   (single binary)  │         HTTP POST + Basic Auth         (Mimir)
└────────────────────┘
```

Each probe node runs **one** binary, managed by **systemd**. No sidecar, no collector, no control-plane service.

## Quick Start

### Prerequisites

- Go 1.22+ (for building from source)
- Root access (for ICMP raw sockets)
- Grafana Cloud account (free tier works)

### Build

```bash
git clone https://github.com/Handgrip/global-telemetry.git
cd global-telemetry
go mod tidy
make build
```

### Configure

1. **Get Grafana Cloud credentials** from [Grafana Cloud Portal](https://grafana.com/):
   - Go to your Stack → Prometheus → Details
   - Note the **Remote Write URL** and **Username** (Metrics instance ID)
   - Create an **API Key** via Cloud Access Policies with `metrics:write` scope

2. **Create agent config** (`/etc/probe-agent/agent.yaml`):

```yaml
probe_name: "tokyo-1"
config_url: "https://raw.githubusercontent.com/you/repo/main/targets.json"
config_refresh_interval: "60s"
push_interval: "60s"
cache_dir: "/var/lib/probe-agent"

grafana_cloud:
  remote_write_url: "https://prometheus-prod-XX-prod-us-central-0.grafana.net/api/prom/push"
  username: "123456"
  api_key: "glc_xxxxx..."
```

3. **Create targets config** (`targets.json`, hosted on any HTTP server):

```json
{
  "defaults": {
    "interval_seconds": 30,
    "timeout_seconds": 5
  },
  "targets": [
    {
      "name": "cloudflare-dns",
      "type": "icmp",
      "host": "1.1.1.1",
      "icmp": { "count": 3 },
      "labels": { "provider": "cloudflare" }
    },
    {
      "name": "example-web",
      "type": "http",
      "url": "https://www.example.com",
      "http": { "method": "GET", "expected_status": 200 },
      "labels": { "type": "web" }
    }
  ]
}
```

### Run

```bash
# Run directly (requires root for ICMP)
sudo ./bin/probe-agent --config /etc/probe-agent/agent.yaml --debug

# Or install as a systemd service
sudo scripts/install.sh
```

### One-Click Install (Linux)

```bash
curl -sSL https://cdn.jsdelivr.net/gh/Handgrip/global-telemetry@main/scripts/install.sh | sudo bash
```

## Metrics Reference

All metrics include labels: `probe`, `target`, `type`, `host`/`url`, plus user-defined labels.

### ICMP Probe

| Metric | Description |
|--------|-------------|
| `probe_success` | 1 = reachable, 0 = unreachable |
| `probe_icmp_rtt_avg_seconds` | Average RTT of N packets |
| `probe_icmp_rtt_min_seconds` | Min RTT |
| `probe_icmp_rtt_max_seconds` | Max RTT |
| `probe_icmp_packet_loss_ratio` | 0.0 to 1.0 |

### HTTP/HTTPS Probe

| Metric | Description |
|--------|-------------|
| `probe_success` | 1 = status matches expected, 0 = fail |
| `probe_http_duration_seconds` | Total request time |
| `probe_http_dns_seconds` | DNS resolution time |
| `probe_http_connect_seconds` | TCP connect time |
| `probe_http_tls_seconds` | TLS handshake time |
| `probe_http_ttfb_seconds` | Time to first byte |
| `probe_http_status_code` | Response status code |
| `probe_http_tls_expiry_seconds` | Seconds until TLS cert expires |

## Config Reference

### Agent Config (`agent.yaml`)

| Field | Default | Description |
|-------|---------|-------------|
| `probe_name` | (required) | Unique name for this probe node |
| `config_url` | (required) | URL to fetch targets.json |
| `config_refresh_interval` | `60s` | How often to re-fetch targets config |
| `push_interval` | `60s` | How often to push metrics to Grafana Cloud |
| `cache_dir` | `/var/lib/probe-agent` | Directory for config cache |
| `grafana_cloud.remote_write_url` | (required) | Prometheus remote write endpoint |
| `grafana_cloud.username` | (required) | Metrics instance ID |
| `grafana_cloud.api_key` | (required) | Cloud Access Policy token |

### Targets Config (`targets.json`)

| Field | Description |
|-------|-------------|
| `defaults.interval_seconds` | Default check interval (default: 30) |
| `defaults.timeout_seconds` | Default probe timeout (default: 5) |
| `targets[].name` | Unique target name (used as label) |
| `targets[].type` | `icmp` or `http` |
| `targets[].host` | Hostname/IP for ICMP targets |
| `targets[].url` | URL for HTTP targets |
| `targets[].icmp.count` | Packets per ICMP check (default: 3) |
| `targets[].http.method` | HTTP method (default: GET) |
| `targets[].http.expected_status` | Expected status code (default: 200) |
| `targets[].http.skip_tls_verify` | Skip TLS certificate verification |
| `targets[].labels` | Custom labels added to all metrics |
| `targets[].interval_seconds` | Per-target override for check interval |
| `targets[].timeout_seconds` | Per-target override for timeout |

## Design Decisions

- **Per-cycle summary, no cross-cycle aggregation.** Each check cycle produces one data point per metric. Grafana Cloud's PromQL handles time-window analysis (`avg_over_time`, `quantile_over_time`).
- **Decoupled check/push intervals.** Probes run every 30s, results buffer in memory, pushed to Grafana Cloud every 60s in one batch HTTP request.
- **Pluggable reporter interface.** The agent internally works with raw probe results. The Prometheus reporter aggregates per-cycle before push. A future ClickHouse reporter can push raw per-packet data.
- **Zero-dependency protobuf encoding.** Prometheus Remote Write protocol is implemented with hand-rolled protobuf wire encoding + snappy compression. No heavy protobuf libraries.
- **Config hot-reload with fallback.** Remote config is refreshed periodically. If unreachable, the agent continues with locally cached config.

## PromQL Examples

```promql
# Average RTT over last hour
avg_over_time(probe_icmp_rtt_avg_seconds{probe="tokyo-1", target="cloudflare-dns"}[1h])

# 95th percentile HTTP duration across all probes
quantile_over_time(0.95, probe_http_duration_seconds{target="example-web"}[1h])

# Availability (uptime ratio) over last 24h
avg_over_time(probe_success{target="cloudflare-dns"}[24h]) * 100

# Targets with packet loss > 5%
probe_icmp_packet_loss_ratio > 0.05

# TLS certificates expiring within 30 days
probe_http_tls_expiry_seconds < 30 * 24 * 3600
```

## License

MIT
