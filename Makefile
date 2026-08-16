# scour — build & quality targets

BINARY      := scour
PKG         := ./cmd/scour
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS     := -ldflags "-X github.com/rangertaha/scour/internal/version.Version=$(VERSION) -X github.com/rangertaha/scour/internal/version.Commit=$(COMMIT)"
GOFILES     := $(shell find . -name '*.go' -not -path './vendor/*')
# Resolve golangci-lint from PATH, else the Go install dir (GOBIN or GOPATH/bin).
GOPATH_BIN  := $(if $(shell go env GOBIN),$(shell go env GOBIN),$(shell go env GOPATH)/bin)
GOLANGCI_LINT ?= $(shell command -v golangci-lint 2>/dev/null || echo $(GOPATH_BIN)/golangci-lint)
SHELL       := /usr/bin/env bash

.DEFAULT_GOAL := help

.PHONY: help all build install test race cover vet fmt fmt-check lint docs docs-serve gate tidy clean run version snapshot

## help: show self-documenting target list
help:
	@awk 'BEGIN {printf "\nUsage:\n  make <target>\n\nTargets:\n"} /^## / {doc = substr($$0, 4); next} /^[a-zA-Z0-9_.-]+:/ {if (doc != "") {split($$1, t, ":"); printf "  %-18s %s\n", t[1], doc; doc = ""}}' $(MAKEFILE_LIST)

## all: run the full check + build pipeline (fmt-check, vet, lint, test, build)
all: fmt-check vet lint test build

## build: compile scour into ./bin
build:
	@mkdir -p bin
	CGO_ENABLED=0 go build -trimpath $(LDFLAGS) -o bin/$(BINARY) $(PKG)

## install: install scour into $GOBIN
install:
	CGO_ENABLED=0 go install -trimpath $(LDFLAGS) $(PKG)

## test: run the test suite
# -timeout 20m, not the default: cmd/scour's interrupt test waits out a lease
# and takes 302s on its own, which runs a package past Go's 10-minute default
# and panics with a timeout that reads as a hang and is not.
test:
	go test -timeout 20m ./...

## race: run the suite under the race detector, shuffled, twice
race:
	go test -race -count=2 -shuffle=on -timeout 30m ./...

## cover: run tests and print a coverage summary
cover:
	go test -coverprofile=coverage.out -timeout 20m ./...
	go tool cover -func=coverage.out | tail -1

## vet: run go vet
vet:
	go vet ./...

## fmt: format every Go file
fmt:
	gofmt -w $(GOFILES)

## fmt-check: fail if anything is not gofmt-clean
fmt-check:
	@out=$$(gofmt -l $(GOFILES)); \
	if [ -n "$$out" ]; then echo "not gofmt-clean:"; echo "$$out"; exit 1; fi

## lint: run golangci-lint (config in .golangci.yml)
lint:
	$(GOLANGCI_LINT) run

## docs: hold the book to the code, in both directions
docs:
	go test ./internal/engine/ -run 'Book|Cover|Readme|Chapter|Figure|Pager' -count=1
	go test ./cmd/scour/ -run 'Documented|Exists|Command|ExitCodes' -count=1

## docs-serve: build and serve the documentation site locally
docs-serve:
	mkdocs serve

## gate: everything that has to pass before a commit
gate: fmt-check vet build docs race

## tidy: tidy and verify go.mod
tidy:
	go mod tidy
	go mod verify

## clean: remove build output and coverage
clean:
	rm -rf bin site coverage.out

## run: build and run scour, e.g. make run ARGS="validate job.hcl"
run: build
	./bin/$(BINARY) $(ARGS)

## version: print the version this build would carry
version:
	@echo $(VERSION) $(COMMIT)

## snapshot: build a local release with GoReleaser, without publishing
snapshot:
	goreleaser release --snapshot --clean
