# Generic Dockerfile for Go applications
# Build stage - using specific hash for better caching
FROM golang@sha256:546671046d6f9f786c24b83111ba1801b736ee4b01b23db33e4f3eb41d4f8883 AS builder

# Accept build arguments
ARG BINARY_PATH
ARG BINARY_NAME

# Copy the pre-built binary from Bazel
COPY ${BINARY_PATH} /app/${BINARY_NAME}
RUN chmod +x /app/${BINARY_NAME}

# Runtime stage - use distroless for security with specific hash
FROM gcr.io/distroless/static-debian12@sha256:6ceafbc2a9c566d66448fb1d5381dede2b29200d1916e03f5238a1c437e7d9ea

# Accept runtime arguments
ARG BINARY_NAME

# Copy the binary from builder stage
COPY --from=builder /app/${BINARY_NAME} /app/${BINARY_NAME}

WORKDIR /app
ENTRYPOINT ["/app/${BINARY_NAME}"]
