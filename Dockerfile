# ChatCLI - Multi-stage Docker build
# Copyright (c) 2024 Edilson Freitas
# License: MIT

# --- Build stage ---
FROM golang:1.27rc2-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /app

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build (TARGETARCH injected by docker buildx for multi-arch)
COPY . .
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -ldflags="-s -w" -o chatcli .

# Build grpc_health_probe with patched dependencies:
#   CVE-2026-34986 (go-jose/v4), GHSA-hrxh-6v49-42gf (grpc),
#   CVE-2026-46600 (x/net), CVE-2026-56852 (x/text)
RUN git clone --depth 1 https://github.com/grpc-ecosystem/grpc-health-probe /tmp/ghp && \
    cd /tmp/ghp && \
    go get github.com/go-jose/go-jose/v4@v4.1.4 \
        google.golang.org/grpc@v1.82.1 \
        golang.org/x/net@v0.57.0 \
        golang.org/x/text@v0.40.0 && \
    go mod tidy && \
    CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -ldflags="-s -w" -o /usr/local/bin/grpc-health-probe . && \
    rm -rf /tmp/ghp

# --- Runtime stage ---
# Distroless static image: zero OS packages, zero CVEs, nonroot by default.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /app/chatcli /usr/local/bin/chatcli
COPY --from=builder /usr/local/bin/grpc-health-probe /usr/local/bin/grpc-health-probe

EXPOSE 50051

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/usr/local/bin/grpc-health-probe", "-addr=:50051"]

ENTRYPOINT ["chatcli", "server"]
