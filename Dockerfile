# ChatCLI - Multi-stage Docker build
# Copyright (c) 2024 Edilson Freitas
# License: MIT

# --- Build stage ---
FROM golang:1.27.2-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /app

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build (TARGETARCH injected by docker buildx for multi-arch)
COPY . .
ARG TARGETARCH
# Release metadata stamped into the binary, the same variables the release
# binaries carry. Without them the server reports version "dev", which is
# what the operator shows in the Instance status.
ARG VERSION=dev
ARG COMMIT_HASH=unknown
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build \
    -ldflags="-s -w -X 'github.com/diillson/chatcli/version.Version=${VERSION}' -X 'github.com/diillson/chatcli/version.CommitHash=${COMMIT_HASH}' -X 'github.com/diillson/chatcli/version.BuildDate=${BUILD_DATE}'" \
    -o chatcli .

# --- Runtime stage ---
# Distroless static image: zero OS packages, zero CVEs, nonroot by default.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /app/chatcli /usr/local/bin/chatcli

EXPOSE 50051

# The server checks itself: `chatcli healthcheck` asks grpc.health.v1 on the
# port and with the TLS settings the server reads (CHATCLI_SERVER_PORT,
# CHATCLI_SERVER_TLS_CERT), so the probe follows any configuration.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/usr/local/bin/chatcli", "healthcheck"]

ENTRYPOINT ["chatcli", "server"]
