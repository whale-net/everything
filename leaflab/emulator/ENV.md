# leaflab-emulator — Environment Variables

> Read this when configuring, deploying, or debugging the board emulator.

## MQTT

| Variable | Default | Required | Description |
|----------|---------|----------|-------------|
| `MQTT_BROKER_URL` | `tcp://localhost:1883` | No | Broker address, e.g. `tcp://rabbitmq-dev.leaflab-local-dev.svc.cluster.local:1883` |
| `MQTT_USERNAME` | `rabbit` | No | Broker user |
| `MQTT_PASSWORD` | `password` | No | Broker password |

## Emulation

| Variable | Default | Required | Description |
|----------|---------|----------|-------------|
| `SCENARIO_DIR` | `leaflab/scripts/scenarios` | No | Directory holding scenario JSON |
| `EMULATOR_BOARDS` | `""` | No | Board spec list (parsing lands in a later task) |
| `PUBLISH_INTERVAL` | `5s` | No | `time.Duration` string (e.g. `5s`, `500ms`) — default reading-publish interval |
| `RANDOM_SEED` | `0` | No | `int64`; `0` means seed from current time |

`PUBLISH_INTERVAL` and `RANDOM_SEED` fail fast at startup if set to a value that cannot be parsed, rather than silently falling back to their defaults.

## Local Development (Tilt)

All values are injected from the Tiltfile. No `.env` file is needed.

```bash
MQTT_BROKER_URL=tcp://rabbitmq-dev.leaflab-local-dev.svc.cluster.local:1883
MQTT_USERNAME=rabbit
MQTT_PASSWORD=password
```
