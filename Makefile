.PHONY: run up build rebuild down restart clean logs fresh test-unit test-unit-race test-integration test-integration-race build-smoke build-smoke-linux smoke-local chat-import-checkpoint zip

CORE_SMOKE_RUN_ID ?=
CHAT_SMOKE_TARGET ?= http://127.0.0.1:8081
CHAT_CORE_TARGET ?= http://core_svc:8080
CHAT_SMOKE_TIMEOUT ?= 10m
CHAT_CHECKPOINT_ARCHIVE ?= $(HOME)/Downloads/yomye-chat.zip

export DOCKER_BUILDKIT=1
export COMPOSE_DOCKER_CLI_BUILD=1

chat-import-checkpoint:
	@test -f "$(CHAT_CHECKPOINT_ARCHIVE)" || { echo "Missing $(CHAT_CHECKPOINT_ARCHIVE)" >&2; exit 1; }
	@chat_import_dir="$$(mktemp -d /tmp/yomye-chat-import.XXXXXX)"; \
	trap 'rm -rf "$$chat_import_dir"' EXIT; \
	unzip -oqq "$(CHAT_CHECKPOINT_ARCHIVE)" -d "$$chat_import_dir"; \
	mapfile -t module_files < <(find "$$chat_import_dir" -type f -name go.mod -not -path '*/vendor/*' -print); \
	chat_root=''; \
	for module_file in "$${module_files[@]}"; do \
		if grep -Eq '^module[[:space:]]+github\.com/vsayfb/gig-platform-chat-service[[:space:]]*$$' "$$module_file"; then \
			test -z "$$chat_root" || { echo 'Archive contains multiple Chat module roots' >&2; exit 1; }; \
			chat_root="$$(dirname "$$module_file")"; \
		fi; \
	done; \
	test -n "$$chat_root" || { echo 'Archive does not contain the Yomye Chat module' >&2; exit 1; }; \
	rsync -a \
		--exclude='.git' --exclude='.env*' --exclude='bin' --exclude='build' \
		--exclude='coverage*' --exclude='Makefile' --exclude='yomye-chat*.zip' \
		"$$chat_root/" "$(CURDIR)/"
	@git status --short

zip:
	./tool/zip.sh

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
	docker exec -i chat-svc ./chat smoke run \
		--run-id "$(CORE_SMOKE_RUN_ID)" \
		--target "$(CHAT_SMOKE_TARGET)" \
		--core-target "$(CHAT_CORE_TARGET)" \
		--environment local \
		--credentials-stdin \
		--timeout "$(CHAT_SMOKE_TIMEOUT)"
