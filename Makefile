.PHONY: dev build test test-race lint vuln tidy docker

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/neotoxicfr/agendarr/internal/server.Version=$(VERSION)

dev:
	AGENDARR_DATA_DIR=./data go run ./cmd/agendarr

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/agendarr ./cmd/agendarr

test:
	go test -timeout 120s ./...

test-race:
	go test -race -timeout 120s ./...

lint:
	golangci-lint run

vuln:
	govulncheck ./...

tidy:
	go mod tidy

docker:
	docker buildx build --build-arg VERSION=$(VERSION) -t agendarr:dev .
