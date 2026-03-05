# Makefile for local tests, lint
# (release is goreleaser from shared workflows)

all: test lint demo

test:
	go test -race ./...

.golangci.yml: Makefile
	curl -fsS -o .golangci.yml https://raw.githubusercontent.com/fortio/workflows/main/golangci.yml

lint: .golangci.yml
	golangci-lint $(DEBUG_LINTERS) run $(LINT_PACKAGES)

coverage:
	go test -race -coverprofile=coverage.out -covermode=atomic ./...

demo:
	go run ./hdr_demo

.PHONY: lint coverage test demo all
