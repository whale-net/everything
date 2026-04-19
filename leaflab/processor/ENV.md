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

Not yet used — wired for future schema writes. Schema is provisioned by `leaflab-migrate`.

| Variable | Default | Description |
|----------|---------|-------------|
| `DB_HOST` | `localhost` | PostgreSQL host |
| `DB_PORT` | `5432` | PostgreSQL port |
| `DB_USER` | `postgres` | PostgreSQL user |
| `DB_PASSWORD` | — | PostgreSQL password |
| `DB_NAME` | `leaflab` | Database name |
| `DB_SSL_MODE` | `disable` | SSL mode (`disable`, `require`, `verify-full`) |

## Local Development (Tilt)

In the local Tilt environment all values are injected from the Tiltfile. No `.env` file is needed.

```bash
RABBITMQ_URL=amqp://rabbit:password@rabbitmq-dev.leaflab-local-dev.svc.cluster.local:5672/
QUEUE_NAME=leaflab-processor
DB_HOST=postgres-dev.leaflab-local-dev.svc.cluster.local
DB_PASSWORD=password
DB_NAME=leaflab
```
