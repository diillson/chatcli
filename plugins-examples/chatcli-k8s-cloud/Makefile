.PHONY: build test clean install

BINARY_NAME=k8s-cloud
VERSION?=1.0.0
COMMIT?=$(shell git rev-parse --short HEAD 2>/dev/null || echo "dev")
LDFLAGS=-ldflags "-X main.version=${VERSION} -X main.commit=${COMMIT}"

build:
	@echo "🔨 Building ${BINARY_NAME}..."
	go build ${LDFLAGS} -o ${BINARY_NAME} .
	@echo "✅ Build complete: bin/${BINARY_NAME}"

install: build
	@echo "📦 Installing ${BINARY_NAME}..."
	cp ${BINARY_NAME} ~/.local/bin/
	@echo "✅ Installed to ~/.local/bin/${BINARY_NAME}"

test:
	@echo "🧪 Running tests..."
	go test -v ./...

test-coverage:
	@echo "📊 Running tests with coverage..."
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out

clean:
	@echo "🧹 Cleaning..."
	rm ${BINARY_NAME}
	rm -f coverage.out
	@echo "✅ Clean complete"

deps:
	@echo "📥 Downloading dependencies..."
	go mod download
	go mod tidy

run-metadata:
	@go run . metadata | jq .

.DEFAULT_GOAL := build