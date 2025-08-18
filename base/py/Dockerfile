FROM python:3.11-slim

# Add a non-root user
RUN useradd -m -d /home/appuser -s /bin/bash appuser
USER appuser
WORKDIR /home/appuser

COPY . .