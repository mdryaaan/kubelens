BINARY     := kubelens
MODULE     := github.com/mdryaaan/kubelens
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X $(MODULE)/pkg/version.Version=$(VERSION) \
	-X $(MODULE)/pkg/version.Commit=$(COMMIT) \
	-X $(MODULE)/pkg/version.BuildDate=$(BUILD_DATE)

GO  ?= go
NPM ?= npm

.DEFAULT_GOAL := help

## help: list the available targets
.PHONY: help
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //' | awk -F': ' '{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

## build: compile the server binary into ./bin
.PHONY: build
build:
	@mkdir -p bin
	$(GO) build -ldflags '$(LDFLAGS)' -o bin/$(BINARY) .
	@echo "built bin/$(BINARY) $(VERSION)"

## build-web: build the dashboard for production
.PHONY: build-web
build-web:
	cd web && $(NPM) run build

## install: install the binary into GOPATH/bin
.PHONY: install
install:
	$(GO) install -ldflags '$(LDFLAGS)' .

# `go test ./...` walks into web/node_modules once a contributor has run npm
# install, because a dependency vendors a Go package of its own.
PKGS = $(shell $(GO) list ./... | grep -v '/web/')

## test: run the Go test suite with coverage
.PHONY: test
test:
	$(GO) test $(PKGS) -race -coverprofile=coverage.out -covermode=atomic
	@$(GO) tool cover -func=coverage.out | tail -1

## cover: write the HTML coverage report
.PHONY: cover
cover: test
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "wrote coverage.html"

## demo: run the whole product against the built-in simulator, no cluster needed
.PHONY: demo
demo: build
	./bin/$(BINARY) serve --demo --explain --provider offline

## dev: run the dashboard against a already-running server
.PHONY: dev
dev:
	cd web && $(NPM) run dev

## eval: score the explanation engine against the labelled corpus
.PHONY: eval
eval: build
	./bin/$(BINARY) eval --provider offline

## eval-ollama: score a real local model against the same corpus
.PHONY: eval-ollama
eval-ollama: build
	./bin/$(BINARY) eval --provider ollama --model llama3

## eval-md: write the evaluation section used in the README
.PHONY: eval-md
eval-md: build
	./bin/$(BINARY) eval --provider offline --format markdown -o eval.md
	@echo "wrote eval.md"

## lint: run go vet and golangci-lint when it is installed
.PHONY: lint
lint:
	$(GO) vet $(PKGS)
	@command -v golangci-lint >/dev/null 2>&1 \
		&& golangci-lint run ./... \
		|| echo "golangci-lint not installed; ran go vet only"

## fmt: format the tree and tidy the module
.PHONY: fmt
fmt:
	gofmt -w .
	$(GO) mod tidy

## clean: remove build output, coverage, and the local incident database
.PHONY: clean
clean:
	rm -rf bin coverage.out coverage.html
	rm -f eval.md eval.json kubelens.db kubelens.db-shm kubelens.db-wal
	rm -rf web/.next web/out

## ci: everything the pipeline runs
.PHONY: ci
ci: fmt lint test eval
