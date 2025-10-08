# Variáveis de versão
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT_HASH ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

# Flags para injetar informações de versão
LD_FLAGS = -X github.com/diillson/chatcli/version.Version=$(VERSION) \
		   -X github.com/diillson/chatcli/version.CommitHash=$(COMMIT_HASH) \
		   -X github.com/diillson/chatcli/version.BuildDate=$(BUILD_DATE)

.PHONY: build
build:
	go build -ldflags "$(LD_FLAGS)" -o bin/chatcli main.go

.PHONY: install
install:
	go install -ldflags "$(LD_FLAGS)"

.PHONY: lint
lint:
	golangci-lint run

.PHONY: test
test:
	go test ./...