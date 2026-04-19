#!/usr/bin/env bash
##
## Initialize FastClaw in cloud mode after first docker compose up.
##
## Usage:  ./init-cloud.sh
##
set -euo pipefail

CONTAINER="${FASTCLAW_CONTAINER:-fastclaw}"
ADMIN_TOKEN="${ADMIN_TOKEN:-$(openssl rand -hex 32)}"

echo "=== Initializing FastClaw Cloud ==="

# Wait for gateway to be ready
echo "Waiting for gateway..."
for i in $(seq 1 30); do
  if docker exec "$CONTAINER" fastclaw doctor >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

# Generate cloud config
echo "Configuring cloud mode..."
docker exec "$CONTAINER" sh -c "cat > /data/.fastclaw/users/local/fastclaw.json" << EOF
{
  "providers": {},
  "agents": {
    "defaults": {
      "model": "gpt-4o",
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
    "rateLimit": {
      "rpm": 60
    },
    "http": {
      "endpoints": {
        "chatCompletions": { "enabled": true },
        "agents": { "enabled": true }
      }
    }
  },
  "storage": {
    "type": "postgres",
    "dsn": "postgres://fastclaw:${POSTGRES_PASSWORD:-fastclaw-secret}@postgres:5432/fastclaw?sslmode=disable",
    "autoMigrate": true
  }
}
EOF

echo ""
echo "=== FastClaw Cloud Ready ==="
echo ""
echo "Admin token: $ADMIN_TOKEN"
echo "Save this token — you need it to manage users and access the admin UI."
echo ""
echo "Next steps:"
echo "  1. Restart:  docker compose restart fastclaw"
echo "  2. Add user: docker compose exec fastclaw fastclaw user add alice --name Alice"
echo "  3. Open:     http://localhost:${FASTCLAW_PORT:-18953}"
echo ""
