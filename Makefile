GO ?= go

ifeq ($(OS),Windows_NT)
SHELL := C:/tools/git/usr/bin/sh.exe
PNPM := cmd.exe /c pnpm
else
PNPM := pnpm
endif
GOFLAGS ?=
VERSION ?= 0.1.0
VERSION := $(if $(VERSION),$(VERSION),0.1.0)
GO_LDFLAGS := -s -w -X backupmanagementcenter/internal/version.Version=$(VERSION)
WEB_DIST := internal/server/webui/dist

PLATFORMS ?= linux/amd64

.PHONY: generate web-build build test dev-server dev-agent clean lint tidy

## generate: regenerate protobuf code (requires protoc + plugins)
generate:
	protoc --go_out=. --go_opt=paths=source_relative \
	       --go-grpc_out=. --go-grpc_opt=paths=source_relative \
	       api/proto/v1/agent.proto

## web-build: build the Vue app into internal/server/webui/dist
web-build:
	cd web && $(PNPM) install --frozen-lockfile && $(PNPM) run build
	rm -rf $(WEB_DIST)
	mkdir -p $(WEB_DIST)
	cp -r web/dist/. $(WEB_DIST)/

## build: web first, then both binaries into bin/
build: web-build
	$(GO) $(GOFLAGS) build -ldflags '$(GO_LDFLAGS)' -o bin/backup-center-server ./cmd/server
	$(GO) $(GOFLAGS) build -ldflags '$(GO_LDFLAGS)' -o bin/backup-center-agent ./cmd/agent

## build-linux: cross-compile Linux binaries
build-linux: web-build
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -ldflags '$(GO_LDFLAGS)' -o bin/backup-center-server ./cmd/server
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -ldflags '$(GO_LDFLAGS)' -o bin/backup-center-agent ./cmd/agent

test:
	$(GO) test ./... -count=1

tidy:
	$(GO) mod tidy

dev-server:
	BMC_DATA_DIR=./data BMC_PUBLIC_URL=http://localhost:5173 $(GO) run ./cmd/server

dev-agent:
	BMC_SERVER_GRPC_URL=127.0.0.1:9090 $(GO) run ./cmd/agent

clean:
	rm -rf bin web/dist $(WEB_DIST)
