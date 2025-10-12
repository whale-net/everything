# Slack Token Configuration Guide

This document explains the two different Slack tokens used in the Friendly Computing Machine and their purposes.

## Token Types

### 1. Slack App Token (`SLACK_APP_TOKEN`)
- **Format**: `xapp-1-...`
- **Purpose**: Socket Mode connection for real-time events
- **Used by**: Main Slack bot service (`run` command)
- **Enables**:
  - Real-time message events
  - Slash command interactions
  - Button/modal interactions
  - Socket Mode bidirectional communication

### 2. Slack Bot Token (`SLACK_BOT_TOKEN`)
- **Format**: `xoxb-...`
- **Purpose**: Web API calls to Slack
- **Used by**:
  - Main Slack bot service (for sending messages)
  - Subscribe service (for notifications)
  - Utility commands (`send-test-command`, `who-am-i`)
- **Enables**:
  - Sending messages (`chat.postMessage`)
  - Opening modals (`views.open`)
  - Getting user/team info
  - All REST API operations

## How They Work Together

1. **Main Bot Service** (`fcm bot run`):
   - Uses **App Token** for Socket Mode to receive events
   - Uses **Bot Token** for Web API to send responses

2. **Subscribe Service** (`fcm bot subscribe`):
   - Only needs **Bot Token** for sending notifications
   - No real-time events needed (uses RabbitMQ instead)

## Environment Variables

```bash
# Required for both services
export SLACK_BOT_TOKEN="xoxb-your-bot-token"

# Required for main bot service only
export SLACK_APP_TOKEN="xapp-1-your-app-token"
```

## Why Two Tokens?

- **Security**: App tokens have broader permissions for Socket Mode
- **Separation**: Web API calls can be made independently of Socket Mode
- **Flexibility**: Subscribe service doesn't need Socket Mode overhead
- **Slack Architecture**: Different parts of Slack API require different authentication

## Getting Tokens

1. **Bot Token**: Go to your Slack app → OAuth & Permissions → Bot User OAuth Token
2. **App Token**: Go to your Slack app → Basic Information → App-Level Tokens

Both tokens need appropriate scopes for your use case.
