# Stage 1: Build static Go binary
FROM golang:1.22-alpine AS builder

WORKDIR /build

# Install git and ca-certificates for dependencies
RUN apk add --no-cache git ca-certificates

# Copy go module files and download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy application source code
COPY cmd/ ./cmd/
COPY pkg/ ./pkg/

# Build statically linked binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -o /build/dp2strm \
    ./cmd/dp2strm

# Stage 2: Final lightweight runtime container
FROM alpine:3.19 AS final

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/dp2strm /app/dp2strm

# Default environment variables
ENV OUTPUT_DIR="/data/strm" \
    PROXY_PORT="8080" \
    PROXY_PUBLIC_URL="http://localhost:8080"

# Create output data directory
RUN mkdir -p /data/strm

# Expose HTTP Playback Proxy Port
EXPOSE 8080

# Default entrypoint runs dp2strm in daemon serving mode using environment variables
ENTRYPOINT ["/app/dp2strm"]
CMD ["--sync-once=false", "--serve=true"]
