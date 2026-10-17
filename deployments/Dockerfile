# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /src

# Allow Go to automatically download newer toolchain if required by dependencies
ENV GOTOOLCHAIN=auto

# Leverage Docker layer caching by downloading Go module dependencies first
COPY go.mod go.sum ./
RUN go mod download

# Copy application source code
COPY . .

# Build the statically linked Aegora binary with stripped symbols and version metadata
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w -X github.com/syncronix-labs/aegora/internal/admin.Version=${VERSION}" \
    -o /aegora \
    ./cmd/aegora

# Final runtime stage
FROM alpine:3.21

# Install runtime dependencies: ca-certificates for upstream HTTPS and wget for container health probes
RUN apk --no-cache add ca-certificates wget tzdata

# Create dedicated non-root runtime group and user
RUN addgroup -g 10001 -S aegora && \
    adduser -u 10001 -S aegora -G aegora

# Copy compiled binary from builder
COPY --from=builder /aegora /aegora

# Expose standard gateway, Prometheus metrics, and admin ports
EXPOSE 8080 9090 9091

# Non-root security context
USER 10001:10001

# Periodic health check verifying data plane liveness
HEALTHCHECK --interval=10s --timeout=3s --retries=3 \
    CMD wget --spider -q http://127.0.0.1:8080/livez || exit 1

# Default command pointing to container config mount point
CMD ["/aegora", "--config", "/etc/aegora/aegora.yaml"]
