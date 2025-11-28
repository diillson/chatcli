.PHONY: build install clean deps test lint

BINARY_NAME=k8s-cloud
VERSION?=2.0.0
COMMIT?=$(shell git rev-parse --short HEAD 2>/dev/null || echo "dev")
LDFLAGS=-ldflags "-X main.version=${VERSION} -X main.commit=${COMMIT}"

build:
	@echo "🔨 Building ${BINARY_NAME} v${VERSION}..."
	@go build ${LDFLAGS} -o ${BINARY_NAME} .
	@echo "✅ Build complete: ${BINARY_NAME}"

install: build
	@echo "📦 Installing ${BINARY_NAME}..."
	@mkdir -p ~/.local/bin
	@cp ${BINARY_NAME} ~/.local/bin/
	@echo "✅ Installed to ~/.local/bin/${BINARY_NAME}"
	@echo "💡 Make sure ~/.local/bin is in your PATH"

clean:
	@echo "🧹 Cleaning..."
	@rm -f ${BINARY_NAME}
	@rm -rf .pulumi/
	@echo "✅ Clean complete"

deps:
	@echo "📥 Downloading dependencies..."
	@go mod download
	@go mod tidy
	@echo "✅ Dependencies ready"

test:
	@echo "🧪 Running tests..."
	@go test -v ./...

lint:
	@echo "🔍 Running linter..."
	@golangci-lint run || echo "⚠️  Install golangci-lint: brew install golangci-lint"

.DEFAULT_GOAL := build