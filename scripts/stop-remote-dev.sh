#!/usr/bin/env bash
set -euo pipefail

project_root="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
runtime_dir="$project_root/.runtime"
frontend_environment="$project_root/frontend/.env.local"

frontend_port=""
if [[ -f "$frontend_environment" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "$frontend_environment"
  set +a
  frontend_port="${VITE_DEV_PORT:-}"
fi

stop_process() {
  local name="$1"
  local pid_file="$runtime_dir/$name.pid"
  if [[ ! -s "$pid_file" ]]; then
    return
  fi

  local pid
  pid="$(<"$pid_file")"
  if kill -0 "$pid" 2>/dev/null; then
    kill "$pid"
    printf 'Stopped %s process (%s).\n' "$name" "$pid"
  fi
  rm -f "$pid_file"
}

stop_orphaned_vite() {
  if [[ -z "$frontend_port" ]] || ! command -v fuser >/dev/null 2>&1; then
    return
  fi

  local pid arguments
  for pid in $(fuser -n tcp "$frontend_port" 2>/dev/null || true); do
    arguments="$(ps -p "$pid" -o args= 2>/dev/null || true)"
    if [[ "$arguments" == *"$project_root/frontend/node_modules/.bin/vite"* || "$arguments" == *"$project_root/frontend/node_modules/vite/"* ]]; then
      kill "$pid"
      printf 'Stopped orphaned Vite process (%s) on port %s.\n' "$pid" "$frontend_port"
    fi
  done
}

stop_process frontend
stop_orphaned_vite
stop_process backend
