.PHONY: run up build rebuild down restart clean logs fresh test-unit test-unit-race test-integration test-integration-race build-smoke build-smoke-linux smoke-local

CORE_SMOKE_RUN_ID ?=
CHAT_SMOKE_TARGET ?= http://127.0.0.1:8081
CHAT_SMOKE_TIMEOUT ?= 10m

export DOCKER_BUILDKIT=1
export COMPOSE_DOCKER_CLI_BUILD=1

run: up

up:
	@echo "Starting chat service environment..."
	docker compose up -d

build:
	@echo "Building chat service..."
	docker compose build

rebuild:
	@echo "Rebuilding and starting chat service..."
	docker compose up -d --build

down:
	@echo "Stopping chat service environment..."
	docker compose down

restart:
	@echo "Restarting chat service..."
	docker compose restart

logs:
	docker compose logs -f

clean:
	@echo "Removing containers and unused images..."
	docker compose down -v --remove-orphans
	docker image prune -f
	# Uncomment the next line only if you want to remove the BuildKit cache.
	# docker builder prune -f

fresh:
	@echo "Performing a clean rebuild..."
	docker compose down -v --remove-orphans
	docker compose build --no-cache
	docker compose up -d --force-recreate

test-unit:
	go test ./...

test-unit-race:
	go test -race ./...

test-integration:
	./integration/run.sh

test-integration-race:
	INTEGRATION_RACE=1 ./integration/run.sh

build-smoke:
	go build -o bin/chat ./cmd/smoke

build-smoke-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/chat-linux-amd64 ./cmd/smoke

smoke-local:
	@test -n "$(CORE_SMOKE_RUN_ID)" || \
		(echo "CORE_SMOKE_RUN_ID is required; use the same run ID already completed by Core" >&2; \
		exit 2)
	@docker exec core_svc ./core smoke credentials \
		--run-id "$(CORE_SMOKE_RUN_ID)" | \
	docker exec -i chat-service ./chat smoke run \
		--run-id "$(CORE_SMOKE_RUN_ID)" \
		--target "$(CHAT_SMOKE_TARGET)" \
		--environment local \
		--credentials-stdin \
		--timeout "$(CHAT_SMOKE_TIMEOUT)"
