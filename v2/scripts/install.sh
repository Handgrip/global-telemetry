#!/usr/bin/env bash
set -euo pipefail

# ─── Global Telemetry v2 Installer ───
# Installs blackbox-exporter + otel-collector-contrib
# and configures them for Grafana Cloud remote write.

INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/global-telemetry"
ENV_FILE="${CONFIG_DIR}/env"

BLACKBOX_VERSION="0.25.0"
OTELCOL_VERSION="0.120.0"

REPO_RAW="https://cdn.jsdelivr.net/gh/Handgrip/global-telemetry@main/v2/configs"

# ─── Helpers ──────────────────────────────────────────────

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; NC='\033[0m'

info()  { echo -e "${GREEN}[INFO]${NC}  $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC}  $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*"; exit 1; }
ask()   { echo -en "${CYAN}[?]${NC} $1: "; }

need_cmd() { command -v "$1" &>/dev/null || error "Required command not found: $1"; }

stop_service_if_running() {
    local svc="$1"
    if [[ "$OS" == "linux" ]] && command -v systemctl &>/dev/null; then
        if systemctl is-active --quiet "${svc}.service" 2>/dev/null; then
            info "Stopping ${svc} before upgrade ..."
            systemctl stop "${svc}.service"
        fi
    fi
}

# ─── Platform Detection ──────────────────────────────────

detect_platform() {
    local os arch
    os="$(uname -s | tr '[:upper:]' '[:lower:]')"
    arch="$(uname -m)"

    case "$os" in
        linux)  OS="linux" ;;
        darwin) OS="darwin" ;;
        *)      error "Unsupported OS: $os" ;;
    esac

    case "$arch" in
        x86_64|amd64)  ARCH="amd64" ;;
        aarch64|arm64) ARCH="arm64" ;;
        *)             error "Unsupported arch: $arch" ;;
    esac

    info "Detected platform: ${OS}/${ARCH}"
}

# ─── Configuration ────────────────────────────────────────

DEFAULT_TARGETS_URL="https://cdn.jsdelivr.net/gh/Handgrip/global-telemetry@main/v2/configs/targets.example.json"

# Read a value: use env var if set, otherwise prompt interactively.
#   read_val VAR_NAME "prompt text" [required|optional]
read_val() {
    local var_name="$1" prompt="$2" required="${3:-required}"
    local current="${!var_name:-}"

    if [[ -n "$current" ]]; then
        info "${prompt}: ${current} (from env)"
        return
    fi

    ask "$prompt"
    read -r "$var_name" <&3
    current="${!var_name:-}"

    if [[ "$required" == "required" && -z "$current" ]]; then
        error "${prompt} is required"
    fi
}

# Same as read_val but hides input (for secrets)
read_secret() {
    local var_name="$1" prompt="$2"
    local current="${!var_name:-}"

    if [[ -n "$current" ]]; then
        info "${prompt}: ******* (from env)"
        return
    fi

    ask "$prompt"
    read -rs "$var_name" <&3
    echo ""
    current="${!var_name:-}"
    [[ -z "$current" ]] && error "${prompt} is required"
}

collect_config() {
    # Load existing env file as defaults (re-install / update scenario)
    if [[ -f "$ENV_FILE" ]]; then
        info "Loading existing config from ${ENV_FILE}"
        set -a; source "$ENV_FILE"; set +a
    fi

    # When piped through "curl | bash", stdin is the curl stream.
    # Read user input from /dev/tty instead.
    if [[ ! -t 0 ]]; then
        exec 3</dev/tty
    else
        exec 3<&0
    fi

    echo ""
    echo -e "${CYAN}══════════════════════════════════════════════════${NC}"
    echo -e "${CYAN}       Global Telemetry v2 — Configuration       ${NC}"
    echo -e "${CYAN}══════════════════════════════════════════════════${NC}"
    echo ""

    # Priority: CLI env var > existing env file > interactive prompt
    #   PROBE_NAME, TARGETS_URL, REMOTE_WRITE_URL,
    #   GRAFANA_USERNAME, GRAFANA_API_KEY

    read_val  PROBE_NAME      "Probe name (unique node identifier, e.g. tokyo-1)"

    echo ""
    echo "  The targets URL must return Content-Type: application/json."
    echo "  (GitHub Raw won't work — use jsDelivr, S3, or your own server)"
    echo ""
    read_val  TARGETS_URL     "Targets URL [Enter = default]" optional
    TARGETS_URL="${TARGETS_URL:-$DEFAULT_TARGETS_URL}"

    echo ""
    echo "  ── Grafana Cloud Credentials ──"
    echo ""
    read_val    REMOTE_WRITE_URL  "Prometheus Remote Write URL"
    read_val    GRAFANA_USERNAME  "Grafana Cloud Username (Metrics instance ID)"
    read_secret GRAFANA_API_KEY   "Grafana Cloud API Key"

    exec 3<&-
    echo ""
    info "Configuration collected."
}

# ─── Download & Install Binaries ─────────────────────────

install_blackbox_exporter() {
    local installed_ver=""
    if [[ -x "${INSTALL_DIR}/blackbox_exporter" ]]; then
        installed_ver=$("${INSTALL_DIR}/blackbox_exporter" --version 2>&1 | head -1 | grep -oP '\d+\.\d+\.\d+' || true)
    fi

    if [[ "$installed_ver" == "$BLACKBOX_VERSION" ]]; then
        info "blackbox_exporter v${BLACKBOX_VERSION} already installed, skipping."
        return
    fi

    [[ -n "$installed_ver" ]] && info "Upgrading blackbox_exporter v${installed_ver} → v${BLACKBOX_VERSION} ..."
    [[ -z "$installed_ver" ]] && info "Downloading blackbox_exporter v${BLACKBOX_VERSION} ..."

    local tarball="blackbox_exporter-${BLACKBOX_VERSION}.${OS}-${ARCH}.tar.gz"
    local url="https://github.com/prometheus/blackbox_exporter/releases/download/v${BLACKBOX_VERSION}/${tarball}"

    local tmpdir
    tmpdir="$(mktemp -d)"
    curl -sSL -o "${tmpdir}/${tarball}" "$url"
    tar -xzf "${tmpdir}/${tarball}" -C "$tmpdir"

    stop_service_if_running blackbox-exporter
    rm -f "${INSTALL_DIR}/blackbox_exporter"
    cp "${tmpdir}/blackbox_exporter-${BLACKBOX_VERSION}.${OS}-${ARCH}/blackbox_exporter" "${INSTALL_DIR}/blackbox_exporter"
    chmod +x "${INSTALL_DIR}/blackbox_exporter"
    rm -rf "$tmpdir"

    info "blackbox_exporter v${BLACKBOX_VERSION} installed to ${INSTALL_DIR}/blackbox_exporter"
}

install_otelcol_contrib() {
    local installed_ver=""
    if [[ -x "${INSTALL_DIR}/otelcol-contrib" ]]; then
        installed_ver=$("${INSTALL_DIR}/otelcol-contrib" --version 2>&1 | head -1 | grep -oP '\d+\.\d+\.\d+' || true)
    fi

    if [[ "$installed_ver" == "$OTELCOL_VERSION" ]]; then
        info "otelcol-contrib v${OTELCOL_VERSION} already installed, skipping."
        return
    fi

    [[ -n "$installed_ver" ]] && info "Upgrading otelcol-contrib v${installed_ver} → v${OTELCOL_VERSION} ..."
    [[ -z "$installed_ver" ]] && info "Downloading otelcol-contrib v${OTELCOL_VERSION} ..."

    local tarball
    if [[ "$OS" == "darwin" ]]; then
        tarball="otelcol-contrib_${OTELCOL_VERSION}_darwin_${ARCH}.tar.gz"
    else
        tarball="otelcol-contrib_${OTELCOL_VERSION}_linux_${ARCH}.tar.gz"
    fi
    local url="https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v${OTELCOL_VERSION}/${tarball}"

    local tmpdir
    tmpdir="$(mktemp -d)"
    curl -sSL -o "${tmpdir}/${tarball}" "$url"
    tar -xzf "${tmpdir}/${tarball}" -C "$tmpdir"

    stop_service_if_running otel-collector
    rm -f "${INSTALL_DIR}/otelcol-contrib"
    cp "${tmpdir}/otelcol-contrib" "${INSTALL_DIR}/otelcol-contrib"
    chmod +x "${INSTALL_DIR}/otelcol-contrib"
    rm -rf "$tmpdir"

    info "otelcol-contrib v${OTELCOL_VERSION} installed to ${INSTALL_DIR}/otelcol-contrib"
}

# ─── Generate Configuration Files ────────────────────────

generate_configs() {
    info "Generating configuration files in ${CONFIG_DIR} ..."
    mkdir -p "$CONFIG_DIR"

    # Download config files from repo (single source of truth: v2/configs/)
    info "Downloading blackbox.yml ..."
    curl -sSL -o "${CONFIG_DIR}/blackbox.yml" "${REPO_RAW}/blackbox.yml"

    info "Downloading otel-collector.yaml ..."
    curl -sSL -o "${CONFIG_DIR}/otel-collector.yaml" "${REPO_RAW}/otel-collector.yaml"

    # Environment file — holds secrets and per-node config
    # OTel Collector reads these via ${env:VAR_NAME} syntax
    cat > "$ENV_FILE" << ENV_EOF
PROBE_NAME=${PROBE_NAME}
TARGETS_URL=${TARGETS_URL}
REMOTE_WRITE_URL=${REMOTE_WRITE_URL}
GRAFANA_USERNAME=${GRAFANA_USERNAME}
GRAFANA_API_KEY=${GRAFANA_API_KEY}
ENV_EOF
    chmod 600 "$ENV_FILE"

    info "Config files written. Secrets stored in ${ENV_FILE} (mode 600)."
}

# ─── Systemd Services ────────────────────────────────────

install_systemd_services() {
    if [[ "$OS" != "linux" ]]; then
        warn "Systemd not available on ${OS}. Skipping service setup."
        echo ""
        echo "  To run manually:"
        echo "    blackbox_exporter --config.file=${CONFIG_DIR}/blackbox.yml"
        echo "    env \$(cat ${ENV_FILE} | xargs) otelcol-contrib --config=${CONFIG_DIR}/otel-collector.yaml"
        echo ""
        return
    fi

    info "Creating systemd services ..."

    # blackbox-exporter.service
    cat > /etc/systemd/system/blackbox-exporter.service << EOF
[Unit]
Description=Blackbox Exporter
Documentation=https://github.com/prometheus/blackbox_exporter
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=${INSTALL_DIR}/blackbox_exporter --config.file=${CONFIG_DIR}/blackbox.yml
Restart=always
RestartSec=5

# Needed for ICMP probes
AmbientCapabilities=CAP_NET_RAW
CapabilityBoundingSet=CAP_NET_RAW

NoNewPrivileges=true
ProtectSystem=full
ProtectHome=true

[Install]
WantedBy=multi-user.target
EOF

    # otel-collector.service
    cat > /etc/systemd/system/otel-collector.service << EOF
[Unit]
Description=OpenTelemetry Collector (contrib)
Documentation=https://opentelemetry.io/docs/collector/
After=network-online.target blackbox-exporter.service
Wants=network-online.target
Requires=blackbox-exporter.service

[Service]
Type=simple
EnvironmentFile=${ENV_FILE}
ExecStart=${INSTALL_DIR}/otelcol-contrib --config=${CONFIG_DIR}/otel-collector.yaml
Restart=always
RestartSec=5

NoNewPrivileges=true
ProtectHome=true

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable blackbox-exporter.service
    systemctl enable otel-collector.service

    # restart picks up new binaries + configs; start is a no-op if already running
    systemctl restart blackbox-exporter.service
    systemctl restart otel-collector.service

    info "Services (re)started: blackbox-exporter, otel-collector"
}

# ─── Summary ─────────────────────────────────────────────

print_summary() {
    echo ""
    echo -e "${GREEN}══════════════════════════════════════════════════${NC}"
    echo -e "${GREEN}         Installation Complete!                   ${NC}"
    echo -e "${GREEN}══════════════════════════════════════════════════${NC}"
    echo ""
    echo "  Components:"
    echo "    • blackbox_exporter  → ${INSTALL_DIR}/blackbox_exporter"
    echo "    • otelcol-contrib    → ${INSTALL_DIR}/otelcol-contrib"
    echo ""
    echo "  Configuration:"
    echo "    • Blackbox config    → ${CONFIG_DIR}/blackbox.yml"
    echo "    • OTel Collector     → ${CONFIG_DIR}/otel-collector.yaml"
    echo "    • Environment/Secrets→ ${CONFIG_DIR}/env"
    echo ""
    echo "  Probe name:  ${PROBE_NAME}"
    echo "  Targets URL: ${TARGETS_URL}"
    echo ""
    if [[ "$OS" == "linux" ]]; then
        echo "  Services:"
        echo "    systemctl status blackbox-exporter"
        echo "    systemctl status otel-collector"
        echo ""
        echo "  Logs:"
        echo "    journalctl -u blackbox-exporter -f"
        echo "    journalctl -u otel-collector -f"
    else
        echo "  Run manually:"
        echo "    blackbox_exporter --config.file=${CONFIG_DIR}/blackbox.yml &"
        echo "    env \$(cat ${ENV_FILE} | xargs) otelcol-contrib --config=${CONFIG_DIR}/otel-collector.yaml"
    fi
    echo ""
    echo "  To update targets, edit your remote targets.json file."
    echo "  Changes will be picked up within 60 seconds (http_sd refresh)."
    echo ""
}

# ─── Main ────────────────────────────────────────────────

main() {
    echo ""
    echo -e "${CYAN}  Global Telemetry v2 Installer${NC}"
    echo -e "${CYAN}  blackbox-exporter + otel-collector + http_sd${NC}"
    echo ""

    need_cmd curl
    need_cmd tar

    detect_platform
    collect_config

    install_blackbox_exporter
    install_otelcol_contrib
    generate_configs
    install_systemd_services
    print_summary
}

main "$@"
