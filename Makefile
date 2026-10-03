VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)

.PHONY: all ui build test run docker compose-up helm-lint

all: ui build

ui:
	cd ui && npm ci && npx tsc -b && npx vite build

build:
	CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=$(VERSION)" -o bin/theophylax ./cmd/theophylax

test:
	go test ./...

# Requires a local Postgres; see deploy/compose for a one-command stack.
run: build
	THEOPHYLAX_DEV_MODE=true THEOPHYLAX_SEED_DEMO=true ./bin/theophylax

docker:
	docker build --build-arg VERSION=$(VERSION) -t theophylax:$(VERSION) .

compose-up:
	docker compose -f deploy/compose/docker-compose.yml up --build

helm-lint:
	helm lint deploy/helm/theophylax
