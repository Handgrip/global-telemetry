# Global Telemetry

Zero-code distributed network monitoring — powered by **blackbox-exporter**, **OpenTelemetry Collector**, and **Prometheus http_sd_configs**.

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│  Remote targets.json (S3 / GitHub Raw / any HTTP endpoint)      │
└──────────────────────────┬──────────────────────────────────────┘
                           │ http_sd_configs (refresh 60s)
                           ▼
┌──────────────────────────────────────────────────────────────┐
│                    OTel Collector (contrib)                    │
│                                                                │
│  ┌─ prometheus receiver ─────────────────────────────────┐    │
│  │  • scrapes blackbox-exporter with discovered targets   │    │
│  │  • scrapes blackbox-exporter own health metrics        │    │
│  └────────────────────────────────────────────────────────┘    │
│  ┌─ hostmetrics receiver ────────────────────────────────┐    │
│  │  • CPU, Memory, Disk, Filesystem, Load, Network, etc.  │    │
│  └────────────────────────────────────────────────────────┘    │
│                           │                                    │
│                   prometheusremotewrite                         │
│                       exporter                                 │
└───────────────────────────┬──────────────────────────────────┘
                            │
                            ▼
                    Grafana Cloud (Mimir)
```

Each node runs two processes:

| Process | Role |
|---------|------|
| **blackbox_exporter** | Executes ICMP / HTTP / TCP / DNS probes |
| **otelcol-contrib** | Scrapes blackbox, collects host metrics, pushes to Grafana Cloud |

No custom code. Configuration only.

## Quick Install

```bash
curl -sSL https://cdn.jsdelivr.net/gh/Handgrip/global-telemetry@main/v2/scripts/bootstrap.sh | sudo bash
```

The bootstrap script auto-detects the latest release tag and fetches the installer via `@{tag}`, bypassing jsDelivr's CDN cache.

The script will ask:

| Prompt | Example |
|--------|---------|
| Probe name | `tokyo-1` |
| Targets URL | `https://raw.githubusercontent.com/you/repo/main/targets.json` |
| Remote Write URL | `https://prometheus-prod-01-prod-ap-southeast-0.grafana.net/api/prom/push` |
| Grafana Username | `123456` |
| Grafana API Key | `glc_...` |

## Targets Format (http_sd)

The targets URL must return a JSON array in [Prometheus http_sd format](https://prometheus.io/docs/prometheus/latest/http_sd/).

> **Important**: The HTTP response must have `Content-Type: application/json`. GitHub Raw (`raw.githubusercontent.com`) returns `text/plain` and will **not** work. Use one of:
> - **jsDelivr**: `https://cdn.jsdelivr.net/gh/USER/REPO@BRANCH/path/targets.json`
> - **S3 / R2 / GCS** (auto-detects `.json` content type)
> - Any web server that returns proper JSON headers

Example:

```json
[
  {
    "targets": ["1.1.1.1"],
    "labels": {
      "__param_module": "icmp",
      "name": "cloudflare-dns",
      "provider": "cloudflare"
    }
  },
  {
    "targets": ["https://www.example.com"],
    "labels": {
      "__param_module": "http_2xx",
      "name": "example-web",
      "category": "web"
    }
  },
  {
    "targets": ["example.com:443"],
    "labels": {
      "__param_module": "tcp_connect",
      "name": "example-tcp"
    }
  }
]
```

### Available Modules

| Module | Prober | Description |
|--------|--------|-------------|
| `icmp` | ICMP | IPv4 ping |
| `icmp6` | ICMP | IPv6 ping (no fallback to v4) |
| `http_2xx` | HTTP | GET request, expects 2xx |
| `http_post_2xx` | HTTP | POST request, expects 2xx |
| `http_2xx_strict_tls` | HTTP | GET, expects 2xx, **fails if not TLS** or cert invalid |
| `http_auth_check` | HTTP | GET, expects 401/403 (service alive behind auth) |
| `tcp_connect` | TCP | TCP connection check |
| `tcp_tls` | TCP | TCP + TLS handshake check |
| `dns_udp` | DNS | DNS A-record resolution over UDP |
| `dns_tcp` | DNS | DNS A-record resolution over TCP |
| `grpc` | gRPC | gRPC health check (plaintext) |
| `grpc_tls` | gRPC | gRPC health check over TLS |

Custom labels in the targets file are preserved as metric labels in Grafana.

## Metrics

### Blackbox Probe Metrics

| Metric | Description |
|--------|-------------|
| `probe_success` | 1 if the probe succeeded |
| `probe_duration_seconds` | Total probe duration |
| `probe_dns_lookup_time_seconds` | DNS resolution time |
| `probe_ip_protocol` | IP protocol used (4 or 6) |
| `probe_http_status_code` | HTTP response status code |
| `probe_http_duration_seconds` | HTTP phase durations |
| `probe_ssl_earliest_cert_expiry` | TLS certificate expiry (unix timestamp) |
| `probe_icmp_duration_seconds` | ICMP round-trip time |

### Host Health Metrics

| Metric | Description |
|--------|-------------|
| `system.cpu.utilization` | CPU utilization ratio |
| `system.memory.utilization` | Memory utilization ratio |
| `system.disk.io` | Disk I/O |
| `system.filesystem.utilization` | Filesystem usage |
| `system.network.io` | Network I/O |
| `system.cpu.load_average.*` | System load averages |
| `system.paging.*` | Swap usage |
| `system.processes.count` | Process count |

## File Locations

| Path | Description |
|------|-------------|
| `/usr/local/bin/blackbox_exporter` | Blackbox exporter binary |
| `/usr/local/bin/otelcol-contrib` | OTel Collector binary |
| `/etc/global-telemetry/blackbox.yml` | Blackbox modules config |
| `/etc/global-telemetry/otel-collector.yaml` | OTel Collector config |
| `/etc/global-telemetry/env` | Environment secrets (mode 600) |

## Service Management (Linux)

```bash
# Status
systemctl status blackbox-exporter
systemctl status otel-collector

# Logs
journalctl -u blackbox-exporter -f
journalctl -u otel-collector -f

# Restart after config change
systemctl restart otel-collector
```

## Updating Targets

Edit your remote `targets.json` file. Changes are picked up automatically within 60 seconds (`http_sd_configs` refresh interval).

## License

MIT
