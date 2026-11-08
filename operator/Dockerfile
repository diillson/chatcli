# ChatCLI Operator - Multi-stage Docker build
# Must be built from the repo root: docker build -f operator/Dockerfile .
# This is required because go.mod uses: replace github.com/diillson/chatcli => ../

FROM golang:1.27.3 AS builder

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
COPY pkg/ pkg/
COPY config/ config/
COPY llm/ llm/
COPY models/ models/
COPY operator/main.go operator/main.go
COPY operator/api/ operator/api/
COPY operator/controllers/ operator/controllers/
COPY operator/internal/ operator/internal/
COPY operator/channels/ operator/channels/
COPY operator/web/ operator/web/

# Build (TARGETARCH is injected by docker buildx for multi-arch)
WORKDIR /workspace/operator
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -a -o manager main.go

# --- Runtime stage ---
# The same distroless static image as the server: no shell, no package
# manager, nonroot by default; it only carries ca-certificates, tzdata and
# base-files. The SourceRepository controller clones over HTTPS and SSH
# in-process (go-git), so the image needs no git or ssh binaries and the
# only thing in it to patch is the manager.
FROM gcr.io/distroless/static-debian13:nonroot

COPY --from=builder /workspace/operator/manager /manager

ENTRYPOINT ["/manager"]
