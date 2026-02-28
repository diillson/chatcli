# ChatCLI - Multi-stage Docker build
# Copyright (c) 2024 Edilson Freitas
# License: MIT

# --- Build stage ---
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /app

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build (TARGETARCH injected by docker buildx for multi-arch)
COPY . .
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -ldflags="-s -w" -o chatcli .

# --- Runtime stage ---
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

# Create non-root user
RUN addgroup -S chatcli && adduser -S chatcli -G chatcli

# Create directories for sessions and plugins
RUN mkdir -p /home/chatcli/.chatcli/sessions /home/chatcli/.chatcli/plugins && \
    chown -R chatcli:chatcli /home/chatcli/.chatcli

COPY --from=builder /app/chatcli /usr/local/bin/chatcli

USER chatcli
WORKDIR /home/chatcli

EXPOSE 50051

# Health check: verify the server process is running and listening
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD pidof chatcli > /dev/null || exit 1

ENTRYPOINT ["chatcli", "server"]
