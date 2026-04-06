# ChatCLI Operator - Multi-stage Docker build
# Must be built from the repo root: docker build -f operator/Dockerfile .
# This is required because go.mod uses: replace github.com/diillson/chatcli => ../

FROM golang:1.25 AS builder

WORKDIR /workspace

# Copy root module files (needed for replace directive)
COPY go.mod go.sum ./

# Copy operator module files and download dependencies (maximizes layer caching)
COPY operator/go.mod operator/go.sum ./operator/
WORKDIR /workspace/operator
RUN go mod download

# Copy source needed by the operator
WORKDIR /workspace
COPY proto/ proto/
COPY operator/main.go operator/main.go
COPY operator/api/ operator/api/
COPY operator/controllers/ operator/controllers/
COPY operator/channels/ operator/channels/
COPY operator/web/ operator/web/

# Build (TARGETARCH is injected by docker buildx for multi-arch)
WORKDIR /workspace/operator
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -a -o manager main.go

# Runtime — uses alpine instead of distroless because the SourceRepository
# controller needs git to clone and sync application repositories.
FROM alpine:3.23 AS runtime
RUN apk add --no-cache git ca-certificates tzdata && \
    adduser -D -u 65532 -g 65532 nonroot
WORKDIR /
COPY --from=builder /workspace/operator/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
