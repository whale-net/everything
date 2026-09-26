# Slack Token Configuration Guide

This document explains the two different Slack tokens used in the Friendly Computing Machine and their purposes.

## Token Types

### 1. Slack App Token (`SLACK_APP_TOKEN`)
- **Format**: `xapp-1-...`
- **Purpose**: Socket Mode connection for real-time events
- **Used by**: Slack bot service (`fcm bot run-slack-socket-app`) and the task pool (`fcm bot run-taskpool`)
- **Enables**:
  - Real-time message events
  - Slash command interactions
  - Button/modal interactions
  - Socket Mode bidirectional communication

### 2. Slack Bot Token (`SLACK_BOT_TOKEN`)
- **Format**: `xoxb-...`
- **Purpose**: Web API calls to Slack
- **Used by**:
  - Slack bot service (for sending messages)
  - Task pool service (for posting scheduled messages, e.g. the weekly music poll)
  - Temporal worker (`fcm workflow run`)
  - Utility commands (`send-test-command`, `who-am-i`)
- **Enables**:
  - Sending messages (`chat.postMessage`)
  - Opening modals (`views.open`)
  - Getting user/team info
  - All REST API operations

## How They Work Together

1. **Slack Bot Service** (`fcm bot run-slack-socket-app`):
   - Uses **App Token** for Socket Mode to receive events
   - Uses **Bot Token** for Web API to send responses

2. **Task Pool Service** (`fcm bot run-taskpool`) and **Temporal Worker** (`fcm workflow run`):
   - Only need the **Bot Token** — they post and read, but hold no Socket Mode connection

## Environment Variables

```bash
# Required by bot, taskpool, and worker
export SLACK_BOT_TOKEN="xoxb-your-bot-token"

# Required for Socket Mode only (bot and taskpool)
export SLACK_APP_TOKEN="xapp-1-your-app-token"
```

## Why Two Tokens?

- **Security**: App tokens have broader permissions for Socket Mode
- **Separation**: Web API calls can be made independently of Socket Mode
- **Flexibility**: The task pool and worker don't need Socket Mode overhead
- **Slack Architecture**: Different parts of Slack API require different authentication

## Getting Tokens

1. **Bot Token**: Go to your Slack app → OAuth & Permissions → Bot User OAuth Token
2. **App Token**: Go to your Slack app → Basic Information → App-Level Tokens

Both tokens need appropriate scopes for your use case.
