#!/usr/bin/env bash
# Starts klovys99 from source without npm: builds/starts the GLiNER Docker
# sidecar, then runs the Go proxy directly with `go run`.
#
# Usage: ./scripts/dev-start.sh
# Requires Docker and Go to be installed locally.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SIDECAR_DIR="$REPO_ROOT/sidecar/gliner"

MODEL="${KLOVIS_GLINER_MODEL:-urchade/gliner_multi_pii-v1}"
REVISION="${KLOVIS_GLINER_MODEL_REVISION:-1fcf13e85f4eef5394e1fcd406cf2ca9ea82351d}"
DATA_DIR="${KLOVIS_GLINER_DATA_DIR:-$HOME/.klovys99/gliner}"
GLINER_URL="${KLOVIS_GLINER_URL:-http://127.0.0.1:8091}"
MANIFEST="$DATA_DIR/model/klovis-model-manifest.json"

mkdir -p "$DATA_DIR"

echo "==> Building GLiNER sidecar image"
docker build -t klovys99-gliner:local "$SIDECAR_DIR"

if ! grep -q "\"revision\": *\"$REVISION\"" "$MANIFEST" 2>/dev/null; then
  echo "==> Downloading GLiNER model ($MODEL @ $REVISION)"
  USER_ARGS=()
  if [[ "$(uname -s)" != MINGW* && "$(uname -s)" != CYGWIN* ]]; then
    USER_ARGS=(--user "$(id -u):$(id -g)")
  fi
  docker run --rm "${USER_ARGS[@]}" \
    -e GLINER_MODEL="$MODEL" \
    -e GLINER_MODEL_REVISION="$REVISION" \
    -e GLINER_MODEL_DIR=/models/model \
    -e HOME=/tmp \
    -e XDG_CACHE_HOME=/tmp/.cache \
    -e HF_HOME=/tmp/.cache/huggingface \
    -v "$DATA_DIR:/models" \
    klovys99-gliner:local \
    python /app/install_model.py
fi

echo "==> Starting GLiNER sidecar container"
KLOVIS_GLINER_MODEL="$MODEL" \
KLOVIS_GLINER_MODEL_REVISION="$REVISION" \
KLOVIS_GLINER_DATA_DIR="$DATA_DIR" \
  docker compose -f "$SIDECAR_DIR/compose.yaml" up -d --no-build

echo "==> Starting klovys99 proxy (go run ./cmd/klovys99)"
cd "$REPO_ROOT"
KLOVIS_GLINER_MODE=full \
KLOVIS_GLINER_URL="$GLINER_URL" \
KLOVIS_GLINER_MODEL="$MODEL" \
KLOVIS_GLINER_MODEL_REVISION="$REVISION" \
  exec go run ./cmd/klovys99 "$@"
