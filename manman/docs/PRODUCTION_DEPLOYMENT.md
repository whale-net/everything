# ManManV2 Secrets and Configuration Guide

This document describes all required secrets, environment variables, and configuration needed to deploy and run ManManV2 services in production.

## Table of Contents

- [Quick Reference](#quick-reference)
- [Database Configuration](#database-configuration)
- [RabbitMQ Configuration](#rabbitmq-configuration)
- [S3/Object Storage Configuration](#s3object-storage-configuration)
- [Service-Specific Configuration](#service-specific-configuration)
- [Production Deployment Checklist](#production-deployment-checklist)
- [Example Configurations](#example-configurations)
- [Security Best Practices](#security-best-practices)

---

## Quick Reference

### Critical Secrets (MUST be provided)

| Secret | Service | Purpose | Required In |
|--------|---------|---------|-------------|
| `DB_PASSWORD` | Processor | PostgreSQL password | **Production** |
| `RABBITMQ_URL` | Processor | RabbitMQ connection string | **Production** |
| `SERVER_ID` | Host | Unique host identifier | **Always** |

### Where to Define Secrets

#### Kubernetes Deployment
```yaml
# Use Kubernetes Secrets
apiVersion: v1
kind: Secret
metadata:
  name: manmanv2-secrets
  namespace: manmanv2
type: Opaque
stringData:
  db-password: "<secure-password>"
  rabbitmq-url: "amqps://user:pass@host:5671/vhost"

---
# Reference in Deployment
env:
- name: DB_PASSWORD
  valueFrom:
    secretKeyRef:
      name: manmanv2-secrets
      key: db-password
```

#### Helm Chart Values
```yaml
# values-production.yaml
secrets:
  dbPassword: "<from-vault-or-secrets-manager>"
  rabbitmqUrl: "<from-vault-or-secrets-manager>"
  s3AccessKey: "<from-vault-or-secrets-manager>"
  s3SecretKey: "<from-vault-or-secrets-manager>"
```

#### Docker Compose
```yaml
# docker-compose.yml
services:
  manmanv2-processor:
    environment:
      DB_PASSWORD: ${DB_PASSWORD}  # From .env file
      RABBITMQ_URL: ${RABBITMQ_URL}
    env_file:
      - .env.production
```

#### Environment Files
```bash
# .env.production (never commit to git!)
DB_PASSWORD=secure-password-here
RABBITMQ_URL=amqps://user:pass@host:5671/vhost
```

---

## Database Configuration

All ManManV2 services (API, Processor, Migration) connect to PostgreSQL.

### Required Variables

#### `DB_PASSWORD`
- **Required For:** Processor (validated on startup)
- **Optional For:** API, Migration (uses default if not set)
- **Format:** String
- **Default:** Empty string
- **Production:** MUST be provided, strong password (16+ chars)
- **Example:** `"xK9mP2qR8vN5tL7wY4bC6fG3"`

### Optional Variables

#### `DB_HOST`
- **Format:** Hostname or IP address
- **Default:** `localhost`
- **Production Example:** `postgres.internal.svc.cluster.local`

#### `DB_PORT`
- **Format:** Integer
- **Default:** `5432`

#### `DB_USER`
- **Format:** String
- **Default:** `postgres`
- **Production Example:** `manmanv2_prod`

#### `DB_NAME`
- **Format:** String
- **Default:** `manman` (API), `postgres` (Migration)
- **Production Example:** `manmanv2`

#### `DB_SSL_MODE`
- **Format:** `disable` | `allow` | `prefer` | `require` | `verify-ca` | `verify-full`
- **Default:** `disable`
- **Production:** `require` or `verify-full`

#### `MIGRATION_TABLE`
- **Service:** Migration only
- **Format:** String
- **Default:** `schema_migrations` (golang-migrate default)

### Connection String Format

Services use individual parameters, not connection strings. The internal format is:
```
host={DB_HOST} port={DB_PORT} user={DB_USER} password={DB_PASSWORD} dbname={DB_NAME} sslmode={DB_SSL_MODE}
```

---

## RabbitMQ Configuration

### Required Variables

#### `RABBITMQ_URL`
- **Required For:** Processor (validated on startup)
- **Optional For:** API, Host (uses default if not set)
- **Format:** AMQP URL string
- **Default (dev):** `amqp://guest:guest@localhost:5672/`
- **Production Format:**
  ```
  amqps://[username]:[password]@[host]:[port]/[vhost]
  ```
- **Production Example:**
  ```
  amqps://manman-user:secure-pass@rabbitmq.prod.svc.cluster.local:5671/manmanv2-prod
  ```

**Important:**
- Use `amqps://` (encrypted) in production, not `amqp://`
- Include vhost in URL: `/manmanv2-prod` (not just `/`)
- Never use `guest:guest` in production

### Optional Variables

#### `QUEUE_NAME`
- **Service:** Processor only
- **Format:** String
- **Default:** `processor-events`

#### `EXTERNAL_EXCHANGE`
- **Service:** Processor only
- **Format:** String
- **Default:** `external`
- **Purpose:** Exchange for publishing events to external subscribers

---

## S3/Object Storage Configuration

### Required for API Service

#### `S3_ACCESS_KEY` and `S3_SECRET_KEY`
- **Service:** API only
- **Format:** String (access key ID and secret)
- **Default:** None (uses AWS SDK credential chain)
- **Required:** Only if using static credentials (not recommended)

**AWS SDK Credential Chain** (in priority order):
1. `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` environment variables
2. `~/.aws/credentials` file
3. `~/.aws/config` file
4. EC2 IAM instance role
5. ECS task IAM role
6. EKS pod service account

**Production Recommendation:** Use IAM roles or Kubernetes service accounts instead of static keys.

### Optional Variables

#### `S3_ENDPOINT`
- **Format:** URL string
- **Default:** Empty (uses AWS S3)
- **Purpose:** S3-compatible storage endpoint
- **Examples:**
  - MinIO: `http://minio:9000` or `https://minio.example.com`
  - OVH: `https://s3.{region}.ovh.net`
  - DigitalOcean Spaces: `https://{region}.digitaloceanspaces.com`

#### `S3_BUCKET`
- **Format:** String
- **Default:** `manman-logs`
- **Production:** Use environment-specific bucket (e.g., `manmanv2-prod-logs`)

#### `S3_REGION`
- **Format:** AWS region code
- **Default:** `us-east-1`
- **Examples:** `us-east-1`, `eu-west-1`, `ap-southeast-1`

---

## Service-Specific Configuration

### API Server

#### `PORT`
- **Format:** Integer or string
- **Default:** `50051`
- **Purpose:** gRPC server listening port

### Processor

#### `HEALTH_CHECK_PORT`
- **Format:** Integer or string
- **Default:** `8080`
- **Purpose:** HTTP health check endpoints (`/healthz`, `/readyz`)

#### `STALE_HOST_THRESHOLD_SECONDS`
- **Format:** Integer
- **Default:** `10`
- **Purpose:** Seconds before marking host as stale
- **Production:** `30` (more tolerant of network issues)

#### `LOG_LEVEL`
- **Format:** `debug` | `info` | `warn` | `error`
- **Default:** `info`
- **Production:** `info` or `warn`

### Host Manager

#### `SERVER_ID`
- **Format:** String or integer
- **Default:** None
- **Required:** YES (validated on startup)
- **Purpose:** Unique identifier for this host instance
- **Examples:** `host-1`, `server-001`, `prod-useast1-host-01`

#### `DOCKER_SOCKET`
- **Format:** File path
- **Default:** `/var/run/docker.sock`
- **Purpose:** Docker socket for container management
- **Production:** Ensure socket is mounted into container

### Migration

#### `MIGRATION_TABLE`
- **Format:** String
- **Default:** `schema_migrations`
- **Purpose:** Table for tracking migration history

---

## Production Deployment Checklist

### 🔐 Secrets Management

- [ ] Generate strong `DB_PASSWORD` (min 16 chars, mixed case, numbers, symbols)
- [ ] Create dedicated RabbitMQ user with strong password
- [ ] Store secrets in vault/secrets manager (not in git or plain files)
- [ ] Rotate secrets regularly (quarterly minimum)
- [ ] Use IAM roles for S3 access (not static keys)
- [ ] Ensure `SERVER_ID` is unique per host deployment

### 🔒 SSL/TLS Configuration

- [ ] Set `DB_SSL_MODE=require` or `verify-full` for PostgreSQL
- [ ] Use `amqps://` (not `amqp://`) for RabbitMQ
- [ ] Use HTTPS endpoints for S3 storage
- [ ] Enable certificate validation in production

### 💾 Database Setup

- [ ] Pre-create database user with minimal required permissions:
  ```sql
  CREATE USER manmanv2_prod WITH PASSWORD 'secure-password';
  CREATE DATABASE manmanv2 OWNER manmanv2_prod;
  GRANT CONNECT ON DATABASE manmanv2 TO manmanv2_prod;
  GRANT ALL PRIVILEGES ON DATABASE manmanv2 TO manmanv2_prod;
  ```
- [ ] Run migrations before starting services
- [ ] Verify database connection before starting services
- [ ] Enable connection pooling
- [ ] Set up database backups

### 📨 RabbitMQ Setup

- [ ] Create dedicated vhost: `/manmanv2-prod`
  ```bash
  rabbitmqctl add_vhost manmanv2-prod
  ```
- [ ] Create dedicated user:
  ```bash
  rabbitmqctl add_user manman-user secure-password
  rabbitmqctl set_permissions -p manmanv2-prod manman-user ".*" ".*" ".*"
  ```
- [ ] Configure persistent message queues
- [ ] Enable RabbitMQ management plugin
- [ ] Set up monitoring and alerts

### 🪣 S3/Storage Setup

- [ ] Create dedicated bucket for logs (e.g., `manmanv2-prod-logs`)
- [ ] Enable versioning on bucket
- [ ] Configure lifecycle policies for log retention
- [ ] Set up bucket encryption (AES-256 or KMS)
- [ ] Enable access logging
- [ ] Configure CORS if needed for web access

### 📊 Monitoring

- [ ] Monitor `/healthz` and `/readyz` endpoints on Processor
- [ ] Set up alerts for stale hosts (> 30 seconds)
- [ ] Monitor database connection pool metrics
- [ ] Track RabbitMQ queue depth and consumer lag
- [ ] Set up logging aggregation (stdout/stderr to log collector)
- [ ] Monitor S3 API error rates

---

## Example Configurations

### Development (Local)

```bash
# PostgreSQL
export DB_HOST="localhost"
export DB_PORT="5432"
export DB_USER="postgres"
export DB_PASSWORD="password"
export DB_NAME="manmanv2"
export DB_SSL_MODE="disable"

# RabbitMQ
export RABBITMQ_URL="amqp://rabbit:password@localhost:5672/manmanv2-dev"

# S3 (MinIO)
export S3_ENDPOINT="http://localhost:9000"
export S3_BUCKET="manmanv2-dev"
export S3_REGION="us-east-1"
export AWS_ACCESS_KEY_ID="minioadmin"
export AWS_SECRET_ACCESS_KEY="minioadmin"

# API
export PORT="50051"

# Processor
export LOG_LEVEL="debug"
export HEALTH_CHECK_PORT="8080"
export STALE_HOST_THRESHOLD_SECONDS="10"

# Host
export SERVER_ID="1"
export DOCKER_SOCKET="/var/run/docker.sock"
```

### Production (AWS EKS)

```bash
# PostgreSQL (RDS)
export DB_HOST="manmanv2-prod.cluster-xyz.us-east-1.rds.amazonaws.com"
export DB_PORT="5432"
export DB_USER="manmanv2_prod"
export DB_PASSWORD="<from-aws-secrets-manager>"
export DB_NAME="manmanv2"
export DB_SSL_MODE="require"

# RabbitMQ (Amazon MQ)
export RABBITMQ_URL="amqps://manman-user:<from-secrets-manager>@b-12345678-1234-1234-1234-123456789012.mq.us-east-1.amazonaws.com:5671/manmanv2-prod"

# S3 (use IAM role via service account - no keys needed)
export S3_BUCKET="manmanv2-prod-logs"
export S3_REGION="us-east-1"
# AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY provided by IAM role

# API
export PORT="50051"

# Processor
export LOG_LEVEL="info"
export HEALTH_CHECK_PORT="8080"
export STALE_HOST_THRESHOLD_SECONDS="30"

# Host (bare metal)
export SERVER_ID="prod-useast1-host-01"
export DOCKER_SOCKET="/var/run/docker.sock"
```

### Production (Kubernetes - Generic)

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: manmanv2-secrets
  namespace: manmanv2
type: Opaque
stringData:
  db-password: "<secure-password>"
  rabbitmq-url: "amqps://user:pass@rabbitmq.internal.svc.cluster.local:5671/manmanv2-prod"
  s3-access-key: "<access-key>"
  s3-secret-key: "<secret-key>"

---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: manmanv2-processor
  namespace: manmanv2
spec:
  template:
    spec:
      containers:
      - name: processor
        image: ghcr.io/whale-net/manman-manmanv2-processor:v1.0.0
        env:
        # Database
        - name: DB_HOST
          value: "postgres.internal.svc.cluster.local"
        - name: DB_PORT
          value: "5432"
        - name: DB_USER
          value: "manmanv2_prod"
        - name: DB_PASSWORD
          valueFrom:
            secretKeyRef:
              name: manmanv2-secrets
              key: db-password
        - name: DB_NAME
          value: "manmanv2"
        - name: DB_SSL_MODE
          value: "require"

        # RabbitMQ
        - name: RABBITMQ_URL
          valueFrom:
            secretKeyRef:
              name: manmanv2-secrets
              key: rabbitmq-url

        # Health Check
        - name: HEALTH_CHECK_PORT
          value: "8080"
        - name: STALE_HOST_THRESHOLD_SECONDS
          value: "30"
        - name: LOG_LEVEL
          value: "info"
```

---

## Security Best Practices

### Secret Storage

❌ **DON'T:**
- Commit secrets to git
- Store secrets in plain text files
- Use default/weak passwords (`guest`, `password`, etc.)
- Share secrets via email or chat
- Hardcode secrets in application code

✅ **DO:**
- Use a secrets management system (AWS Secrets Manager, HashiCorp Vault, Kubernetes Secrets)
- Rotate secrets regularly (quarterly minimum)
- Use strong, randomly generated passwords (16+ characters)
- Use IAM roles/service accounts instead of static credentials
- Encrypt secrets at rest
- Audit secret access

### Database Security

- Use dedicated database user per environment
- Grant minimal required permissions (principle of least privilege)
- Enable SSL/TLS for database connections
- Use connection pooling with appropriate limits
- Monitor for slow queries and connection leaks
- Regular security patches and updates

### RabbitMQ Security

- Create dedicated vhost per environment
- Use unique credentials per environment
- Enable SSL/TLS (amqps://)
- Configure message TTL and queue limits
- Enable access control and authorization
- Monitor queue depth and consumer lag

### S3 Storage Security

- Use IAM roles instead of static credentials
- Enable bucket encryption
- Configure bucket policies for least privilege
- Enable versioning for data protection
- Set up lifecycle policies for cost optimization
- Monitor access patterns and unauthorized access attempts

### Network Security

- Use private networks/VPCs for inter-service communication
- Implement network policies in Kubernetes
- Use service mesh for mTLS between services
- Restrict ingress/egress rules
- Enable DDoS protection
- Use API gateways for external access

---

## Troubleshooting

### Common Issues

#### "Database password is required"
- **Service:** Processor
- **Cause:** `DB_PASSWORD` environment variable not set
- **Fix:** Set `DB_PASSWORD` environment variable with valid PostgreSQL password

#### "RabbitMQ connection failed: access denied"
- **Cause:** Invalid credentials or insufficient vhost permissions
- **Fix:**
  1. Verify `RABBITMQ_URL` format includes correct username/password
  2. Check user has permissions on vhost: `rabbitmqctl list_permissions -p manmanv2-prod`

#### "S3 operation failed: credentials not found"
- **Service:** API
- **Cause:** No valid AWS credentials found in credential chain
- **Fix:**
  1. Set `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` environment variables, OR
  2. Configure IAM role for service account, OR
  3. Mount AWS credentials file into container

#### "Host validation failed: SERVER_ID is required"
- **Service:** Host
- **Cause:** `SERVER_ID` not set
- **Fix:** Set unique `SERVER_ID` for this host instance

### Validation Commands

#### Test Database Connection
```bash
psql "host=${DB_HOST} port=${DB_PORT} user=${DB_USER} dbname=${DB_NAME} sslmode=${DB_SSL_MODE}" -c "SELECT version();"
```

#### Test RabbitMQ Connection
```bash
# Extract components from RABBITMQ_URL
rabbitmqadmin -H <host> -P <port> -u <user> -p <pass> list vhosts
```

#### Test S3 Access
```bash
aws s3 ls s3://${S3_BUCKET}/ --region ${S3_REGION}
```

---

## References

- **Codebase Locations:**
  - API Configuration: `manman/api/main.go`
  - Processor Configuration: `manman/processor/config.go`
  - Host Configuration: `manman/host/main.go`
  - Migration Configuration: `libs/go/migrate/cli.go`
  - S3 Library: `libs/go/s3/s3.go`
  - RabbitMQ Library: `libs/go/rmq/connection.go`

- **Documentation:**
  - [PostgreSQL SSL Documentation](https://www.postgresql.org/docs/current/libpq-ssl.html)
  - [RabbitMQ Access Control](https://www.rabbitmq.com/access-control.html)
  - [AWS SDK Go v2 Credentials](https://aws.github.io/aws-sdk-go-v2/docs/configuring-sdk/)
  - [Kubernetes Secrets](https://kubernetes.io/docs/concepts/configuration/secret/)

---

**Last Updated:** 2026-02-07
**Maintained By:** ManManV2 Team
