# leaflab-processor — Environment Variables

> Read this when configuring, deploying, or debugging the processor service.

## RabbitMQ

| Variable | Default | Required | Description |
|----------|---------|----------|-------------|
| `RABBITMQ_URL` | — | Yes | AMQP URL, e.g. `amqp://rabbit:password@host:5672/` |
| `QUEUE_NAME` | `leaflab-processor` | No | AMQP queue name to declare and consume from |

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
