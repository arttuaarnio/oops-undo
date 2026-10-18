VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test lint bench

build:
	go build -ldflags "-X main.version=$(VERSION)" -o bin/oops ./cmd/oops

test:
	go test ./...

lint:
	golangci-lint run

bench:
	go test -run '^$$' -bench . ./...
