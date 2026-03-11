#!/usr/bin/env bash
set -euo pipefail

# Global Probe Agent - One-click installer
# Usage: curl -sSL https://raw.githubusercontent.com/OWNER/REPO/main/scripts/install.sh | bash

REPO="Handgrip/global-telemetry"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/probe-agent"
CACHE_DIR="/var/lib/probe-agent"
SERVICE_NAME="probe-agent"
BINARY_NAME="probe-agent"

info()  { echo "[INFO]  $*"; }
error() { echo "[ERROR] $*" >&2; exit 1; }

detect_platform() {
    OS=$(uname -s | tr '[:upper:]' '[:lower:]')
    ARCH=$(uname -m)
    case "$ARCH" in
        x86_64|amd64) ARCH="amd64" ;;
        aarch64|arm64) ARCH="arm64" ;;
        *) error "Unsupported architecture: $ARCH" ;;
    esac
    case "$OS" in
        linux) ;;
        darwin) ;;
        *) error "Unsupported OS: $OS" ;;
    esac
    info "Detected platform: ${OS}/${ARCH}"
}

get_latest_version() {
    VERSION=$(curl -sSf "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | head -1 | sed -E 's/.*"([^"]+)".*/\1/')
    if [ -z "$VERSION" ]; then
        error "Failed to fetch latest release version"
    fi
    info "Latest version: ${VERSION}"
}

download_binary() {
    DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${VERSION}/${BINARY_NAME}_${OS}_${ARCH}.tar.gz"
    info "Downloading ${DOWNLOAD_URL}"

    TMP_DIR=$(mktemp -d)
    trap "rm -rf $TMP_DIR" EXIT

    curl -sSfL "$DOWNLOAD_URL" -o "${TMP_DIR}/archive.tar.gz"
    tar -xzf "${TMP_DIR}/archive.tar.gz" -C "$TMP_DIR"

    install -m 755 "${TMP_DIR}/${BINARY_NAME}" "${INSTALL_DIR}/${BINARY_NAME}"
    info "Installed ${INSTALL_DIR}/${BINARY_NAME}"
}

setup_config() {
    mkdir -p "$CONFIG_DIR" "$CACHE_DIR"

    if [ ! -f "${CONFIG_DIR}/agent.yaml" ]; then
        cat > "${CONFIG_DIR}/agent.yaml" <<'YAML'
probe_name: "changeme"
config_url: "https://raw.githubusercontent.com/OWNER/REPO/main/targets.json"
config_refresh_interval: "60s"
push_interval: "60s"
cache_dir: "/var/lib/probe-agent"

grafana_cloud:
  remote_write_url: "https://prometheus-prod-XX-prod-us-central-0.grafana.net/api/prom/push"
  username: "YOUR_METRICS_INSTANCE_ID"
  api_key: "YOUR_CLOUD_ACCESS_POLICY_TOKEN"
YAML
        info "Created default config at ${CONFIG_DIR}/agent.yaml"
    else
        info "Config already exists at ${CONFIG_DIR}/agent.yaml, skipping"
    fi
}

install_systemd() {
    if ! command -v systemctl &>/dev/null; then
        info "systemd not found, skipping service installation"
        info "You can run the agent manually: ${INSTALL_DIR}/${BINARY_NAME} --config ${CONFIG_DIR}/agent.yaml"
        return
    fi

    cat > "/etc/systemd/system/${SERVICE_NAME}.service" <<EOF
[Unit]
Description=Probe Agent - Global Network Monitor
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=${INSTALL_DIR}/${BINARY_NAME} --config ${CONFIG_DIR}/agent.yaml
Restart=always
RestartSec=5
User=root
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable "${SERVICE_NAME}"
    info "Systemd service installed and enabled"
}

main() {
    if [ "$(id -u)" -ne 0 ]; then
        error "This script must be run as root (use sudo)"
    fi

    info "Installing Global Probe Agent..."
    detect_platform
    get_latest_version
    download_binary
    setup_config
    install_systemd

    echo ""
    echo "============================================"
    echo "  Installation complete!"
    echo "============================================"
    echo ""
    echo "Next steps:"
    echo "  1. Edit ${CONFIG_DIR}/agent.yaml with your settings:"
    echo "     - Set probe_name (e.g. 'tokyo-1')"
    echo "     - Set config_url to your targets.json URL"
    echo "     - Set grafana_cloud credentials"
    echo ""
    echo "  2. Start the agent:"
    echo "     systemctl start ${SERVICE_NAME}"
    echo ""
    echo "  3. Check status:"
    echo "     systemctl status ${SERVICE_NAME}"
    echo "     journalctl -u ${SERVICE_NAME} -f"
    echo ""
}

main "$@"
