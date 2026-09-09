#!/usr/bin/env bash
set -euo pipefail

project_root="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
runtime_dir="$project_root/.runtime"

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

stop_process frontend
stop_process backend
