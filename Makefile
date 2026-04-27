BINARY := gcam
PREFIX ?= $(shell go env GOPATH)/bin

.PHONY: all build run test vet fmt fmt-check lint tidy clean check install help

.DEFAULT_GOAL := all

all: check build lint ## default: fmt-check + vet + test + build + lint

help: ## list targets
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-12s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## build $(BINARY)
	go build -o $(BINARY) .

run: ## go run .
	go run .

test: ## go test ./...
	go test ./...

vet: ## go vet ./...
	go vet ./...

fmt: ## gofmt -w
	gofmt -w .

fmt-check: ## fail if any file needs gofmt
	@out=$$(gofmt -l .); \
	if [ -n "$$out" ]; then \
	    echo "gofmt needed:"; echo "$$out"; exit 1; \
	fi

lint: ## golangci-lint run (requires golangci-lint)
	@command -v golangci-lint >/dev/null 2>&1 || { \
	    echo "golangci-lint not installed — see https://golangci-lint.run/usage/install/"; \
	    exit 1; \
	}
	golangci-lint run

tidy: ## go mod tidy
	go mod tidy

clean: ## remove build artefacts
	rm -f $(BINARY)

check: fmt-check vet test ## fmt-check + vet + test (no external tools)

install: build ## install $(BINARY) into $(PREFIX)
	install -m 0755 $(BINARY) $(PREFIX)/$(BINARY)
