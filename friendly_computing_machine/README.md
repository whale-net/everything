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
Required to use the whagent-net `@mention` integration (see [docs/whagent_integration.md](docs/whagent_integration.md)):
```bash
WHAGENT_API_URL=<whagent-net api gRPC address, e.g. localhost:50054>
WHAGENT_UI_PUBLIC_URL=<whagent-net ui base URL, e.g. https://whagent.example.com>
WHAGENT_KEYCLOAK_TOKEN_URL=<Keycloak token endpoint>
WHAGENT_CLIENT_ID=<fcm's service-account client id>
WHAGENT_CLIENT_SECRET=<fcm's service-account client secret>
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

## Slash commands
- `/wai <prompt>` — AI answer grounded in the channel's recent messages.
- `/wpoll` — opens a form to build a Simple Poll-style poll with vote buttons; `/wpoll "Question?" "Option 1" "Option 2" [anonymous] [limit N]` creates one inline. See [docs/poll.md](docs/poll.md).

## AI agent sessions
`@mention` the bot in a channel linked to a whagent-net agent to start a threaded AI session. See [docs/whagent_integration.md](docs/whagent_integration.md).

## Logging, Tracing, and Metrics
Logging uses the standard library with optional OTLP export. Tracing relies on Opentelemetry auto-instrumentation; the Python SDK is still experimental, so breaking changes may occur. Auto-instrumentation for logging proved unreliable, so logging remains manual. Metrics are currently out of scope. An OTEL collector should ingest signals according to the Helm values. Python keeps logging to stdout, though you can disable it if needed.

## Additional Notes
To bootstrap Opentelemetry auto-instrumentation outside Tilt:
```bash
uv run opentelemetry-bootstrap -a requirements | uv pip install --requirement -
```

Reference commit for the final task-pool-heavy version: https://github.com/whale-net/friendly-computing-machine/releases/tag/taskpool-last-stop