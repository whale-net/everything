# Generic Dockerfile for Go applications
FROM golang:latest AS builder

# Accept build arguments
ARG BINARY_PATH
ARG BINARY_NAME

# Copy the pre-built binary from Bazel
COPY ${BINARY_PATH} /app/${BINARY_NAME}
RUN chmod +x /app/${BINARY_NAME}

# Runtime stage - use distroless for security
FROM gcr.io/distroless/static-debian12:latest

# Accept runtime arguments
ARG BINARY_NAME

# Copy the binary from builder stage
COPY --from=builder /app/${BINARY_NAME} /app/${BINARY_NAME}

WORKDIR /app
ENTRYPOINT ["/app/${BINARY_NAME}"]
