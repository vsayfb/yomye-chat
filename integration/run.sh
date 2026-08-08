#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_FILE="${ROOT_DIR}/integration/docker-compose.yml"
PROJECT_NAME="chat-service-integration"

compose() {
    docker compose --project-name "${PROJECT_NAME}" -f "${COMPOSE_FILE}" "$@"
}

cleanup() {
    if [[ "${KEEP_INTEGRATION_CONTAINERS:-0}" != "1" ]]; then
        compose down -v --remove-orphans >/dev/null 2>&1 || true
    fi
}
trap cleanup EXIT

command -v docker >/dev/null 2>&1 || {
    echo "docker is required for integration tests" >&2
    exit 1
}

docker compose version >/dev/null

compose down -v --remove-orphans >/dev/null 2>&1 || true
compose up -d mongodb

# The integration suite requires transaction-capable MongoDB. Initiate the
# single-node replica set and wait until it becomes writable before Go tests run.
for _ in $(seq 1 60); do
    if compose exec -T mongodb mongosh --quiet --port 27018 --eval \
        'try { if (db.hello().isWritablePrimary) { quit(0) }; rs.initiate({_id:"rs0",members:[{_id:0,host:"mongodb:27018"}]}); } catch (e) {} quit(1)' \
        >/dev/null 2>&1; then
        break
    fi
    sleep 1
done

if ! compose exec -T mongodb mongosh --quiet --port 27018 --eval \
    'if (!db.hello().isWritablePrimary) { quit(1) }' >/dev/null 2>&1; then
    echo "integration MongoDB did not become a writable replica-set primary" >&2
    compose logs mongodb >&2 || true
    exit 1
fi

compose run --rm test-runner \
    go test -count=1 -tags=integration ./integration/...
