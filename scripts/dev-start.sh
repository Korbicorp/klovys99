#!/usr/bin/env bash
# Starts the full development runtime: prepares the GLiNER model on first run,
# then launches the AI Workspace frontend and the root compose stack.
#
# Usage: ./scripts/dev-start.sh
# Requires Docker to be installed locally.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SIDECAR_DIR="$REPO_ROOT/sidecar/gliner"
PRESIDIO_DIR="$REPO_ROOT/sidecar/presidio"
ENV_FILE="$REPO_ROOT/.env"
ENV_EXAMPLE="$REPO_ROOT/.env.example"

if [[ ! -f "$ENV_FILE" ]]; then
  if [[ ! -f "$ENV_EXAMPLE" ]]; then
    echo "Missing $ENV_EXAMPLE" >&2
    exit 1
  fi
  echo "==> Creating .env from .env.example"
  cp "$ENV_EXAMPLE" "$ENV_FILE"
fi

set -a
source "$ENV_FILE"
set +a

MODEL="${KLOVIS_GLINER_MODEL:-urchade/gliner_multi_pii-v1}"
REVISION="${KLOVIS_GLINER_MODEL_REVISION:-1fcf13e85f4eef5394e1fcd406cf2ca9ea82351d}"
DATA_DIR="${KLOVIS_GLINER_DATA_DIR:-$REPO_ROOT/.data/gliner}"
MANIFEST="$DATA_DIR/model/klovis-model-manifest.json"

mkdir -p "$DATA_DIR"

echo "==> Building GLiNER sidecar image"
docker build -t klovys99-gliner:local "$SIDECAR_DIR"

if [[ "${KLOVIS_PRESIDIO_MODE:-full}" == "full" ]]; then
  echo "==> Building Presidio sidecar image"
  docker build -t klovys99-presidio:local "$PRESIDIO_DIR"
fi

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

cd "$REPO_ROOT"
echo "==> Starting AI Workspace frontend"
npm run ui:ai-workspace &
AI_WORKSPACE_PID=$!

cleanup() {
  kill "$AI_WORKSPACE_PID" 2>/dev/null || true
  wait "$AI_WORKSPACE_PID" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

echo "==> Starting root Docker Compose stack"
docker compose up --build "$@"
