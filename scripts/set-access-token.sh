#!/usr/bin/env bash
set -euo pipefail

project_root="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
environment_file="$project_root/.env"

if [[ -f "$environment_file" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "$environment_file"
  set +a
fi

access_token_file="${ACCESS_TOKEN_FILE:-.runtime/access-token}"
if [[ "$access_token_file" != /* ]]; then
  access_token_file="$project_root/$access_token_file"
fi

usage() {
  cat <<'EOF'
Usage:
  bash scripts/set-access-token.sh             # enter a token without echoing it
  bash scripts/set-access-token.sh --generate  # generate and display a new token
  bash scripts/set-access-token.sh --stdin     # read a token from standard input

The token file is chosen by ACCESS_TOKEN_FILE in .env.
EOF
}

token=""
show_generated_token=false
case "${1:-}" in
  "")
    read -r -s -p "New access token: " token
    printf '\n'
    read -r -s -p "Confirm access token: " confirmation
    printf '\n'
    if [[ "$token" != "$confirmation" ]]; then
      printf 'Tokens do not match. Nothing was changed.\n' >&2
      exit 1
    fi
    ;;
  --generate)
    if ! command -v openssl >/dev/null 2>&1; then
      printf 'openssl is required to generate an access token.\n' >&2
      exit 1
    fi
    token="$(openssl rand -base64 32 | tr -d '\n')"
    show_generated_token=true
    ;;
  --stdin)
    IFS= read -r token || true
    ;;
  -h|--help)
    usage
    exit 0
    ;;
  *)
    usage >&2
    exit 1
    ;;
esac

if [[ -z "${token//[[:space:]]/}" ]]; then
  printf 'Access token must not be empty. Nothing was changed.\n' >&2
  exit 1
fi
if [[ "$token" == *$'\n'* || "$token" == *$'\r'* ]]; then
  printf 'Access token must be one line. Nothing was changed.\n' >&2
  exit 1
fi

token_directory="$(dirname -- "$access_token_file")"
umask 077
mkdir -p "$token_directory"
temporary_file="$(mktemp "$token_directory/.access-token.XXXXXX")"
trap 'rm -f -- "$temporary_file"' EXIT
printf '%s\n' "$token" >"$temporary_file"
chmod 600 "$temporary_file"

if [[ -e "$access_token_file" ]]; then
  chmod --reference="$access_token_file" "$temporary_file"
  chown --reference="$access_token_file" "$temporary_file" 2>/dev/null || true
fi

mv -f -- "$temporary_file" "$access_token_file"
trap - EXIT

printf 'Access token updated: %s\n' "$access_token_file"
if [[ "$show_generated_token" = true ]]; then
  printf 'New access token (save it now): %s\n' "$token"
fi
