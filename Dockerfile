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

# Build statically linked binary (0 CGO, pure Go modernc.org/sqlite)
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-w -s" \
    -o /build/dp2strm \
    ./cmd/dp2strm

# Stage 2: Final lightweight runtime container
FROM alpine:3.19 AS final

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/dp2strm /app/dp2strm

# Create default output data directory
RUN mkdir -p /data/strm

# Expose HTTP Playback Handler Port
EXPOSE 8080

# Run dp2strm daemon
ENTRYPOINT ["/app/dp2strm"]
