#!/usr/bin/env bash
##
## FastClaw Cloud — One-click start (idempotent, multi-instance)
##
## Each port is an independent FastClaw instance with its own data.
##
## Usage:
##   ./start.sh                          # interactive, prompts for port + API key
##   ./start.sh --port 19000             # specify port
##   ./start.sh --port 19000 --reset     # wipe and reinitialize instance on port 19000
##   ./start.sh --port 19000 --rebuild   # rebuild image, keep data
##   LLM_API_KEY=sk-xxx ./start.sh --port 19000
##
set -euo pipefail
cd "$(dirname "$0")"

# --- Parse args ---
PORT=""
FORCE_REBUILD=false
RESET=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --port)    PORT="$2"; shift 2 ;;
    --rebuild) FORCE_REBUILD=true; shift ;;
    --reset)   RESET=true; shift ;;
    *)         echo "Unknown arg: $1"; exit 1 ;;
  esac
done

# --- Interactive port selection if not specified ---
if [ -z "$PORT" ]; then
  echo ""
  echo "FastClaw Cloud — Choose a port for this instance"
  echo ""
  # Show existing instances
  EXISTING=$(ls -d instances/*/  2>/dev/null | sed 's|instances/||;s|/||' || true)
  if [ -n "$EXISTING" ]; then
    echo "  Existing instances:"
    for p in $EXISTING; do
      LABEL=$(cat "instances/$p/label" 2>/dev/null || echo "")
      echo "    port $p  $LABEL"
    done
    echo ""
  fi
  printf "  Port [18953]: "
  read -r PORT
  PORT="${PORT:-18953}"
fi

# --- Instance directory (each port = independent data) ---
INSTANCE_DIR="instances/$PORT"
TOKEN_FILE="$INSTANCE_DIR/admin-token"
INITIALIZED_FLAG="$INSTANCE_DIR/initialized"
PROJECT_NAME="fastclaw-$PORT"
PG_PORT=$((PORT + 1000))  # e.g. 18953 → 19953

mkdir -p "$INSTANCE_DIR"

# --- Reset if requested ---
if $RESET; then
  echo "=== Resetting instance on port $PORT ==="
  COMPOSE_PROJECT_NAME=$PROJECT_NAME docker compose down -v 2>/dev/null || true
  rm -rf "$INSTANCE_DIR"
  mkdir -p "$INSTANCE_DIR"
fi

# --- Instance label ---
if [ ! -f "$INSTANCE_DIR/label" ]; then
  printf "  Instance label (optional, e.g. 'production'): "
  read -r LABEL
  echo "$LABEL" > "$INSTANCE_DIR/label"
fi

POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-fastclaw-$(openssl rand -hex 8)}"
if [ -f "$INSTANCE_DIR/pg-password" ]; then
  POSTGRES_PASSWORD=$(cat "$INSTANCE_DIR/pg-password")
else
  echo "$POSTGRES_PASSWORD" > "$INSTANCE_DIR/pg-password"
fi

# --- .env for this instance ---
cat > .env <<EOF
FASTCLAW_PORT=$PORT
POSTGRES_PASSWORD=$POSTGRES_PASSWORD
EOF

# --- docker-compose override for this instance's ports ---
cat > "$INSTANCE_DIR/docker-compose.override.yml" <<EOF
services:
  fastclaw:
    ports:
      - "$PORT:18953"
  postgres:
    ports:
      - "$PG_PORT:5432"
EOF

# --- Build image (shared across instances) ---
if $FORCE_REBUILD || ! docker image inspect fastclaw/fastclaw:latest >/dev/null 2>&1; then
  echo "=== Building FastClaw Docker image ==="
  docker compose build
else
  echo "=== Image exists, skipping build (use --rebuild to force) ==="
fi

# --- Start services ---
echo "=== Starting instance on port $PORT ==="
COMPOSE_PROJECT_NAME=$PROJECT_NAME docker compose -f docker-compose.yml -f "$INSTANCE_DIR/docker-compose.override.yml" up -d

# --- Wait for Postgres ---
printf "Waiting for Postgres"
for i in $(seq 1 30); do
  if COMPOSE_PROJECT_NAME=$PROJECT_NAME docker compose -f docker-compose.yml -f "$INSTANCE_DIR/docker-compose.override.yml" exec -T postgres pg_isready -U fastclaw >/dev/null 2>&1; then
    echo " ready."
    break
  fi
  printf "."
  sleep 1
done

# --- Already initialized? Just start. ---
if [ -f "$INITIALIZED_FLAG" ]; then
  ADMIN_TOKEN=$(cat "$TOKEN_FILE" 2>/dev/null || echo "unknown")
  echo ""
  echo "============================================"
  echo "  FastClaw Cloud (port $PORT) is running!"
  echo "============================================"
  echo ""
  echo "  Web UI:      http://localhost:$PORT"
  echo "  Admin token: $ADMIN_TOKEN"
  echo ""
  echo "  Manage users:"
  EXEC_CMD="COMPOSE_PROJECT_NAME=$PROJECT_NAME docker compose -f docker-compose.yml -f $INSTANCE_DIR/docker-compose.override.yml exec fastclaw"
  echo "    $EXEC_CMD fastclaw user list"
  echo "    $EXEC_CMD fastclaw user add alice --name Alice"
  echo ""
  echo "  Stop:    COMPOSE_PROJECT_NAME=$PROJECT_NAME docker compose -f docker-compose.yml -f $INSTANCE_DIR/docker-compose.override.yml down"
  echo "  Reset:   ./start.sh --port $PORT --reset"
  echo "============================================"
  exit 0
fi

# ===== First-time initialization =====
echo ""
echo "=== First-time setup for port $PORT ==="

# --- LLM Provider ---
LLM_PROVIDER="${LLM_PROVIDER:-openai}"
LLM_API_BASE="${LLM_API_BASE:-}"
LLM_MODEL="${LLM_MODEL:-gpt-4o}"

if [ -z "${LLM_API_KEY:-}" ]; then
  echo ""
  echo "  Enter your LLM API key (or press Enter to skip):"
  printf "  API key: "
  read -r LLM_API_KEY
fi

ADMIN_TOKEN=$(openssl rand -hex 32)
echo "$ADMIN_TOKEN" > "$TOKEN_FILE"

# --- Build provider JSON ---
PROVIDER_JSON=""
if [ -n "${LLM_API_KEY:-}" ]; then
  API_BASE_FIELD=""
  if [ -n "$LLM_API_BASE" ]; then
    API_BASE_FIELD="\"apiBase\": \"$LLM_API_BASE\","
  fi
  PROVIDER_JSON="\"$LLM_PROVIDER\": { $API_BASE_FIELD \"apiKey\": \"$LLM_API_KEY\" }"
fi

COMPOSE="COMPOSE_PROJECT_NAME=$PROJECT_NAME docker compose -f docker-compose.yml -f $INSTANCE_DIR/docker-compose.override.yml"

# --- Write cloud config ---
sleep 3
eval "$COMPOSE exec -T fastclaw sh -c 'mkdir -p /data/.fastclaw/users/local'"

eval "$COMPOSE exec -T fastclaw sh -c 'cat > /data/.fastclaw/users/local/fastclaw.json'" <<EOF
{
  "providers": { $PROVIDER_JSON },
  "agents": {
    "defaults": {
      "model": "$LLM_MODEL",
      "maxTokens": 8192,
      "temperature": 0.7,
      "maxToolIterations": 20
    },
    "list": []
  },
  "channels": {},
  "gateway": {
    "mode": "cloud",
    "bind": "all",
    "port": 18953,
    "auth": {
      "mode": "token",
      "token": "$ADMIN_TOKEN"
    },
    "rateLimit": { "rpm": 60 },
    "http": {
      "endpoints": {
        "chatCompletions": { "enabled": true },
        "agents": { "enabled": true }
      }
    }
  }
}
EOF

# --- Restart ---
eval "$COMPOSE restart fastclaw"
sleep 3

# --- Create demo user ---
DEMO_OUTPUT=$(eval "$COMPOSE exec -T fastclaw fastclaw user add demo --name 'Demo User'" 2>&1)
DEMO_TOKEN=$(echo "$DEMO_OUTPUT" | grep "Token:" | awk '{print $2}')

if [ -n "${LLM_API_KEY:-}" ]; then
  eval "$COMPOSE exec -T fastclaw sh -c \"
    cd /data/.fastclaw/users/demo
    cat fastclaw.json | sed 's/\\\"providers\\\": {}/\\\"providers\\\": { $PROVIDER_JSON }/' | sed 's/\\\"model\\\": \\\"gpt-4o\\\"/\\\"model\\\": \\\"$LLM_MODEL\\\"/' > tmp.json && mv tmp.json fastclaw.json
  \"" 2>/dev/null || true
fi

touch "$INITIALIZED_FLAG"

echo ""
echo "============================================"
echo "  FastClaw Cloud (port $PORT) is running!"
echo "============================================"
echo ""
echo "  Web UI:      http://localhost:$PORT"
echo ""
echo "  Admin token: $ADMIN_TOKEN"
echo "  Demo token:  $DEMO_TOKEN"
echo "  LLM:         $LLM_PROVIDER ($LLM_MODEL)"
echo ""
echo "  Create more users:"
echo "    $COMPOSE exec fastclaw fastclaw user add alice --name Alice"
echo ""
echo "  Stop:    $COMPOSE down"
echo "  Restart: ./start.sh --port $PORT"
echo "  Reset:   ./start.sh --port $PORT --reset"
echo "============================================"
