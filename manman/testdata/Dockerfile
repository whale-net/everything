# Test game server container for wrapper integration tests
FROM alpine:3.19

# Install bash for better script compatibility
RUN apk add --no-cache bash

# Copy the test server script
COPY test_game_server.sh /app/server.sh
RUN chmod +x /app/server.sh

# Run the test server
CMD ["/app/server.sh"]
