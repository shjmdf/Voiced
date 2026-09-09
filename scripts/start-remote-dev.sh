#!/usr/bin/env bash
set -euo pipefail

project_root="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
runtime_dir="$project_root/.runtime"
backend_environment="$project_root/.env"
frontend_environment="$project_root/frontend/.env.local"

for required_file in "$backend_environment" "$frontend_environment"; do
  if [[ ! -f "$required_file" ]]; then
    printf 'Missing configuration file: %s\n' "$required_file" >&2
    exit 1
  fi
done

set -a
# shellcheck disable=SC1090
source "$backend_environment"
# shellcheck disable=SC1090
source "$frontend_environment"
set +a

if [[ -n "${NODE_BIN_DIR:-}" ]]; then
  export PATH="$NODE_BIN_DIR:$PATH"
fi

for command in go npm; do
  if ! command -v "$command" >/dev/null 2>&1; then
    printf 'Required command is not available: %s\n' "$command" >&2
    exit 1
  fi
done

for value in LISTEN_ADDR FRONTEND_ORIGIN VITE_DEV_HOST VITE_DEV_PORT VITE_BACKEND_URL; do
  if [[ -z "${!value:-}" ]]; then
    printf '%s must be set in the local environment files.\n' "$value" >&2
    exit 1
  fi
done

mkdir -p "$runtime_dir/logs"

frontend_path() {
  if [[ "$1" = /* ]]; then
    printf '%s\n' "$1"
    return
  fi
  printf '%s/frontend/%s\n' "$project_root" "$1"
}

tls_enabled=false
if [[ -n "${DEV_HTTPS_KEY_FILE:-}" || -n "${DEV_HTTPS_CERT_FILE:-}" ]]; then
  if [[ -z "${DEV_HTTPS_KEY_FILE:-}" || -z "${DEV_HTTPS_CERT_FILE:-}" ]]; then
    printf 'DEV_HTTPS_KEY_FILE and DEV_HTTPS_CERT_FILE must be set together.\n' >&2
    exit 1
  fi
  tls_enabled=true
  key_file="$(frontend_path "$DEV_HTTPS_KEY_FILE")"
  certificate_file="$(frontend_path "$DEV_HTTPS_CERT_FILE")"

  if [[ ! -f "$key_file" || ! -f "$certificate_file" ]]; then
    if [[ -z "${DEV_HTTPS_PUBLIC_NAME:-}" ]]; then
      printf 'DEV_HTTPS_PUBLIC_NAME is required to create a self-signed certificate.\n' >&2
      exit 1
    fi
    if ! command -v openssl >/dev/null 2>&1; then
      printf 'openssl is required to create a self-signed certificate.\n' >&2
      exit 1
    fi
    mkdir -p "$(dirname "$key_file")" "$(dirname "$certificate_file")"
    if [[ "$DEV_HTTPS_PUBLIC_NAME" =~ ^[0-9]{1,3}(\.[0-9]{1,3}){3}$ ]]; then
      subject_alt_name="IP:$DEV_HTTPS_PUBLIC_NAME"
    else
      subject_alt_name="DNS:$DEV_HTTPS_PUBLIC_NAME"
    fi
    openssl req -x509 -newkey rsa:2048 -nodes -sha256 -days 14 \
      -keyout "$key_file" \
      -out "$certificate_file" \
      -subj "/CN=$DEV_HTTPS_PUBLIC_NAME" \
      -addext "subjectAltName=$subject_alt_name" >/dev/null 2>&1
    printf 'Created a 14-day self-signed certificate for %s.\n' "$DEV_HTTPS_PUBLIC_NAME"
  fi
fi

process_is_running() {
  local pid_file="$1"
  [[ -s "$pid_file" ]] && kill -0 "$(<"$pid_file")" 2>/dev/null
}

if process_is_running "$runtime_dir/backend.pid" || process_is_running "$runtime_dir/frontend.pid"; then
  printf 'Remote development is already running. Run bash scripts/stop-remote-dev.sh first.\n' >&2
  exit 1
fi

rm -f "$runtime_dir/backend.pid" "$runtime_dir/frontend.pid"

(
  cd "$project_root"
  go build -o "$runtime_dir/voiced-server" ./cmd/server
)

nohup "$runtime_dir/voiced-server" >"$runtime_dir/logs/backend.log" 2>&1 < /dev/null &
backend_pid=$!
printf '%s\n' "$backend_pid" >"$runtime_dir/backend.pid"

(
  cd "$project_root/frontend"
  nohup npm run dev >"$runtime_dir/logs/frontend.log" 2>&1 < /dev/null &
  printf '%s\n' "$!" >"$runtime_dir/frontend.pid"
)

sleep 1
if ! process_is_running "$runtime_dir/backend.pid" || ! process_is_running "$runtime_dir/frontend.pid"; then
  printf 'A process exited during startup. Check %s/logs.\n' "$runtime_dir" >&2
  exit 1
fi

scheme=http
if [[ "$tls_enabled" = true ]]; then
  scheme=https
fi
printf 'Remote development is running. Frontend: %s://%s:%s\n' "$scheme" "${DEV_HTTPS_PUBLIC_NAME:-VPS_IP}" "$VITE_DEV_PORT"
printf 'Logs: %s/logs\n' "$runtime_dir"
