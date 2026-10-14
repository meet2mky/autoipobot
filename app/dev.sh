#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"
export PATH="$(go env GOPATH)/bin:$PATH"

if ! command -v air >/dev/null 2>&1; then
  echo "Installing air..."
  go install github.com/air-verse/air@latest
fi

exec air -c .air.toml
