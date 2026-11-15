.PHONY: test run up down lint

test:
	go test -race ./...

lint:
	go vet ./...

run:
	go run ./cmd/server

up:
	docker compose up --build

down:
	docker compose down -v
