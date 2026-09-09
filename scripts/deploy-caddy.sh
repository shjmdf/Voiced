#!/usr/bin/env bash
set -euo pipefail

go_version="1.24.11"
node_version="24.20.0"
project_root="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"

domain=""
udp_port_min="46100"
udp_port_max="46999"
ssh_port="31422"
enable_ufw=false

usage() {
  cat <<'EOF'
Deploy Voiced behind Caddy on Debian or Ubuntu.

Usage:
  sudo bash scripts/deploy-caddy.sh --domain voiced.example.com [options]

Options:
  --domain NAME       Public domain for the site (required)
  --udp-min PORT      First WebRTC UDP port (default: 46100)
  --udp-max PORT      Last WebRTC UDP port (default: 46999)
  --ssh-port PORT     Existing SSH port to preserve in UFW (default: 31422)
  --enable-ufw        Enable UFW after adding required rules
  -h, --help          Show this help

Before running the script:
  1. Point the domain A record at this VPS.
  2. Allow TCP 80, TCP 443, and the selected UDP range in the cloud firewall.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --domain)
      domain="${2:-}"
      shift 2
      ;;
    --udp-min)
      udp_port_min="${2:-}"
      shift 2
      ;;
    --udp-max)
      udp_port_max="${2:-}"
      shift 2
      ;;
    --ssh-port)
      ssh_port="${2:-}"
      shift 2
      ;;
    --enable-ufw)
      enable_ufw=true
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      printf 'Unknown option: %s\n\n' "$1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

if [[ "${EUID}" -ne 0 ]]; then
  printf 'Run this script with sudo.\n' >&2
  exit 1
fi
if [[ ! -f "$project_root/go.mod" || ! -f "$project_root/frontend/package.json" ]]; then
  printf 'Run this script from a complete Voiced checkout.\n' >&2
  exit 1
fi
if [[ ! -x "$project_root/scripts/set-access-token.sh" ]]; then
  printf 'scripts/set-access-token.sh is missing. Pull the current main branch first.\n' >&2
  exit 1
fi
if [[ -z "$domain" || ! "$domain" =~ ^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+$ ]]; then
  printf '%s\n' '--domain must be a valid public domain name.' >&2
  exit 1
fi

validate_port() {
  local name="$1"
  local value="$2"
  if [[ ! "$value" =~ ^[0-9]+$ ]] || ((value < 1 || value > 65535)); then
    printf '%s must be a port from 1 to 65535.\n' "$name" >&2
    exit 1
  fi
}

validate_port "--udp-min" "$udp_port_min"
validate_port "--udp-max" "$udp_port_max"
validate_port "--ssh-port" "$ssh_port"
if ((udp_port_min > udp_port_max)); then
  printf '%s\n' '--udp-min must not be greater than --udp-max.' >&2
  exit 1
fi

case "$(dpkg --print-architecture)" in
  amd64)
    go_arch="amd64"
    go_checksum="bceca00afaac856bc48b4cc33db7cd9eb383c81811379faed3bdbc80edb0af65"
    node_arch="x64"
    ;;
  arm64)
    go_arch="arm64"
    go_checksum="beaf0c51cbe0bd71b828a2c6b9a2c0b11cd86aa58672691ef2f1de88eb621de"
    node_arch="arm64"
    ;;
  *)
    printf 'This script supports amd64 and arm64 Debian/Ubuntu hosts.\n' >&2
    exit 1
    ;;
esac

export DEBIAN_FRONTEND=noninteractive

printf 'Installing operating-system packages...\n'
apt-get update
apt-get install -y ca-certificates curl git gnupg \
  debian-keyring debian-archive-keyring apt-transport-https build-essential ufw

printf 'Installing Caddy...\n'
curl -1sLf https://dl.cloudsmith.io/public/caddy/stable/gpg.key -o /tmp/caddy-stable.gpg.key
gpg --dearmor --yes -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg /tmp/caddy-stable.gpg.key
curl -1sLf https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt \
  -o /etc/apt/sources.list.d/caddy-stable.list
chmod o+r /usr/share/keyrings/caddy-stable-archive-keyring.gpg
chmod o+r /etc/apt/sources.list.d/caddy-stable.list
apt-get update
apt-get install -y caddy

printf 'Installing Go %s...\n' "$go_version"
go_archive="go${go_version}.linux-${go_arch}.tar.gz"
curl -fSLo "/tmp/$go_archive" "https://go.dev/dl/$go_archive"
printf '%s  %s\n' "$go_checksum" "/tmp/$go_archive" | sha256sum -c -
rm -rf /usr/local/go
tar -C /usr/local -xzf "/tmp/$go_archive"
ln -sfn /usr/local/go/bin/go /usr/local/bin/go
ln -sfn /usr/local/go/bin/gofmt /usr/local/bin/gofmt

printf 'Installing Node.js %s...\n' "$node_version"
node_archive="node-v${node_version}-linux-${node_arch}.tar.xz"
curl -fSLo "/tmp/$node_archive" "https://nodejs.org/dist/v${node_version}/$node_archive"
curl -fSLo /tmp/node-shasums.txt "https://nodejs.org/dist/v${node_version}/SHASUMS256.txt"
grep "  $node_archive$" /tmp/node-shasums.txt | sed "s|  |  /tmp/|" | sha256sum -c -
rm -rf "/opt/node-v${node_version}-linux-${node_arch}"
tar -C /opt -xJf "/tmp/$node_archive"
ln -sfn "/opt/node-v${node_version}-linux-${node_arch}" /opt/node
ln -sfn /opt/node/bin/node /usr/local/bin/node
ln -sfn /opt/node/bin/npm /usr/local/bin/npm
ln -sfn /opt/node/bin/npx /usr/local/bin/npx

if ! id -u voiced >/dev/null 2>&1; then
  useradd --system --create-home --home-dir /opt/voiced --shell /usr/sbin/nologin voiced
fi
chown -R voiced:voiced "$project_root"

printf 'Building backend and frontend...\n'
runuser -u voiced -- env HOME="$project_root" PATH="/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin" \
  bash -lc "cd '$project_root' && go mod download && go test ./... && mkdir -p bin && go build -trimpath -ldflags='-s -w' -o bin/voiced-server ./cmd/server"
runuser -u voiced -- env HOME="$project_root" PATH="/usr/local/bin:/usr/bin:/bin" \
  bash -lc "cd '$project_root/frontend' && npm ci && VITE_API_BASE_URL='https://$domain' VITE_WS_BASE_URL='wss://$domain' npm run build"
chmod -R a+rX "$project_root/frontend/dist"

install -d -o voiced -g voiced -m 750 /etc/voiced
cat >/etc/voiced/voiced.env <<EOF
LISTEN_ADDR=127.0.0.1:8082
FRONTEND_ORIGIN=https://$domain
ACCESS_TOKEN_FILE=/etc/voiced/access-token
WEBRTC_UDP_PORT_MIN=$udp_port_min
WEBRTC_UDP_PORT_MAX=$udp_port_max
WEBRTC_STUN_URLS=stun:stun.l.google.com:19302
VOICED_DEBUG=false
EOF
chown root:voiced /etc/voiced/voiced.env
chmod 640 /etc/voiced/voiced.env

if [[ ! -s /etc/voiced/access-token ]]; then
  printf 'Generating the access token. Save the value printed next.\n'
  runuser -u voiced -- env ACCESS_TOKEN_FILE=/etc/voiced/access-token \
    bash "$project_root/scripts/set-access-token.sh" --generate
fi

cat >/etc/systemd/system/voiced.service <<EOF
[Unit]
Description=Voiced voice-room backend
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=voiced
Group=voiced
WorkingDirectory=$project_root
EnvironmentFile=/etc/voiced/voiced.env
ExecStart=$project_root/bin/voiced-server
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF

cat >/etc/caddy/Caddyfile <<EOF
$domain {
    handle /api/* {
        reverse_proxy 127.0.0.1:8082
    }

    handle /ws/* {
        reverse_proxy 127.0.0.1:8082
    }

    handle /health {
        reverse_proxy 127.0.0.1:8082
    }

    handle {
        root * $project_root/frontend/dist
        try_files {path} /index.html
        file_server
    }
}
EOF

caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
systemctl daemon-reload
systemctl enable --now voiced
systemctl restart voiced
systemctl enable --now caddy
systemctl reload caddy

ufw allow "$ssh_port/tcp"
ufw allow 80/tcp
ufw allow 443/tcp
ufw allow "$udp_port_min:$udp_port_max/udp"
if [[ "$enable_ufw" = true ]]; then
  ufw --force enable
fi

printf '\nDeployment completed.\n'
printf 'Backend: systemctl status voiced\n'
printf 'Proxy and certificates: systemctl status caddy\n'
printf 'Open: https://%s\n' "$domain"
if [[ "$enable_ufw" = false ]]; then
  printf 'UFW rules were added but UFW was not enabled; use sudo ufw enable when ready.\n'
fi
