.PHONY: run up build rebuild down restart clean logs fresh

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