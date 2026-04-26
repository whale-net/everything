# leaflab-processor — Environment Variables

> Read this when configuring, deploying, or debugging the processor service.

## RabbitMQ

| Variable | Default | Required | Description |
|----------|---------|----------|-------------|
| `RABBITMQ_URL` | — | Yes | AMQP URL. Use `amqps://` scheme for SSL, e.g. `amqps://rabbit:password@rmq.whalenet.dev:5671/` |
| `QUEUE_NAME` | `leaflab-processor` | No | AMQP queue name to declare and consume from |
| `RABBITMQ_SSL_VERIFY` | `true` | No | Set to `false` to skip certificate verification (insecure, dev only) |
| `RABBITMQ_CA_CERT_PATH` | — | No | Path to a PEM CA certificate for self-signed/private-CA setups |
| `RABBITMQ_TLS_SERVER_NAME` | — | No | SNI override — set to `rmq.whalenet.dev` when connecting via internal hostname but cert is for the external domain |

SSL is handled automatically when `RABBITMQ_URL` uses the `amqps://` scheme. For local dev the plain `amqp://` scheme is used and no SSL vars are needed.

The processor binds to the `amq.topic` exchange with routing key `leaflab.#`.
RabbitMQ's MQTT plugin routes MQTT topics to this exchange, converting `/` → `.`
(e.g. `leaflab/abc/sensor/light` → `leaflab.abc.sensor.light`).

## Database

| Variable | Default | Required | Description |
|----------|---------|----------|-------------|
| `PG_DATABASE_URL` | — | Yes | PostgreSQL connection string, e.g. `postgres://user:pass@host:5432/leaflab?sslmode=disable` |

Schema is provisioned by `leaflab-migrate` before the processor starts.

## Local Development (Tilt)

All values are injected from the Tiltfile. No `.env` file is needed.

```bash
RABBITMQ_URL=amqp://rabbit:password@rabbitmq-dev.leaflab-local-dev.svc.cluster.local:5672/
QUEUE_NAME=leaflab-processor
PG_DATABASE_URL=postgres://postgres:password@postgres-dev.leaflab-local-dev.svc.cluster.local:5432/leaflab?sslmode=disable
```
