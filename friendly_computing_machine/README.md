# friendly-computing-machine
slackbot

## Environment Setup
Use `uv` for dependency management:
```bash
uv venv
source .venv/bin/activate
uv sync
```
Optionally point your IDE at `./.venv/bin/python`.

Install the Temporal CLI from https://docs.temporal.io/cli if you plan to run the worker locally.

### Environment Variables
Copy `.env.example` into the project root as `.env` and fill in the values:
```bash
cp .env.example .env
```
Required settings:
```bash
SLACK_BOT_TOKEN=<BOT token from slack>
SLACK_APP_TOKEN=<APP token from slack>
POSTGRES_URL=postgresql+psycopg2://username:password@host:port/database
GOOGLE_API_KEY=https://aistudio.google.com/app/apikey
APP_ENV=dev
TEMPORAL_HOST=localhost:7233
```
Load them before running the CLI:
```bash
export $(cat .env | xargs)
```

## Running Locally
Start a Temporal dev server:
```bash
temporal server start-dev
```

Run the combined Slack bot + task pool:
```bash
uv run fcm bot run
```

Run the Temporal worker:
```bash
uv run workflow run
```

## Logging, Tracing, and Metrics
Logging uses the standard library with optional OTLP export. Tracing relies on Opentelemetry auto-instrumentation; the Python SDK is still experimental, so breaking changes may occur. Auto-instrumentation for logging proved unreliable, so logging remains manual. Metrics are currently out of scope. An OTEL collector should ingest signals according to the Helm values. Python keeps logging to stdout, though you can disable it if needed.

## Additional Notes
To bootstrap Opentelemetry auto-instrumentation outside Tilt:
```bash
uv run opentelemetry-bootstrap -a requirements | uv pip install --requirement -
```

Reference commit for the final task-pool-heavy version: https://github.com/whale-net/friendly-computing-machine/releases/tag/taskpool-last-stop