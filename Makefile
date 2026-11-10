TEST_DATABASE_URL ?= postgres://scheduler:scheduler@localhost:5432/scheduler?sslmode=disable

.PHONY: up down test test-integration run

up: ## build and start the API and Postgres on :8080
	docker compose up --build

down:
	docker compose down -v

test: ## unit tests (no database needed)
	go test ./...

test-integration: ## unit + Postgres tests (starts Postgres in Docker)
	docker compose up -d --wait db
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test -count=1 -race ./...

run: ## run the API locally against the compose Postgres
	docker compose up -d --wait db
	SEED_FILE=appointments.json go run ./cmd/server
