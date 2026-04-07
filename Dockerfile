# ChatCLI - Multi-stage Docker build
# Copyright (c) 2024 Edilson Freitas
# License: MIT

# --- Build stage ---
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /app

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build (TARGETARCH injected by docker buildx for multi-arch)
COPY . .
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -ldflags="-s -w" -o chatcli .

# Build grpc_health_probe for Docker HEALTHCHECK (standalone/compose environments)
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
    go install github.com/grpc-ecosystem/grpc-health-probe@latest && \
    cp "$(go env GOPATH)/bin/grpc-health-probe" /usr/local/bin/grpc-health-probe

# --- Runtime stage ---
# Distroless static image: zero OS packages, zero CVEs, nonroot by default.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /app/chatcli /usr/local/bin/chatcli
COPY --from=builder /usr/local/bin/grpc-health-probe /usr/local/bin/grpc-health-probe

EXPOSE 50051

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/usr/local/bin/grpc-health-probe", "-addr=:50051"]

ENTRYPOINT ["chatcli", "server"]
