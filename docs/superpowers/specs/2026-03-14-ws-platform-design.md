# WebSocket Platform Adapter Design

## Overview

Add a generic WebSocket platform to cc-connect, enabling users to interact with Claude Code (or any agent) through a browser-based chat UI. The platform starts an HTTP/WebSocket server; visiting the root URL serves an embedded chat page, and `/ws` handles bidirectional messaging.

## Target User

Single developer, personal use. No authentication, no multi-user session isolation.

## File Structure

```
platform/ws/
├── ws.go              # Platform implementation: HTTP server, WebSocket upgrade, message dispatch
├── hub.go             # Connection manager: register/unregister clients, broadcast/unicast
└── static/
    └── index.html     # Embedded chat UI (single file, pure HTML/CSS/JS)

cmd/cc-connect/
└── plugin_platform_ws.go   # //go:build !no_ws — blank import for registration
```

## Configuration

```toml
[[projects.platforms]]
type = "ws"
[projects.platforms.options]
addr = "127.0.0.1:9000"   # Listen address, default 127.0.0.1:9000
allow_from = "*"           # User filter (personal use: allow all)
```

- Browser: `http://127.0.0.1:9000`
- WebSocket: `ws://127.0.0.1:9000/ws`

## WebSocket Protocol

All messages are JSON. Fields not applicable to a message type are omitted.

### Client → Server

| type | fields | description |
|------|--------|-------------|
| `message` | `content` | User sends a chat message |
| `permission_response` | `request_id`, `allow` (bool) | User responds to a permission prompt |

### Server → Client

| type | fields | description |
|------|--------|-------------|
| `reply` | `content` | Final agent reply |
| `thinking` | `content` | Agent thinking/reasoning (truncated per display config) |
| `tool_use` | `content` | Agent tool invocation info |
| `permission` | `content`, `request_id` | Permission request requiring user action |
| `update` | `content`, `message_id` | Streaming preview update to an existing message |

### Connection Management

- Session key: `ws:default` (fixed, single-user)
- Heartbeat: WebSocket native ping/pong, 60s read deadline
- Client reconnect: JS auto-retry every 3 seconds

## Embedded Chat UI

Single `index.html` file, embedded via `//go:embed`, zero external dependencies.

### Features

- Message input with Enter to send, Shift+Enter for newline
- Message list with user/AI distinction, basic Markdown rendering
- Thinking/Tool Use shown as collapsible sections
- Permission requests as cards with Allow/Deny buttons
- Connection status indicator (connected/disconnected/reconnecting)
- Auto-scroll to bottom
- Dark theme, terminal-style aesthetic

### Not Included

- Session management UI (use `/new`, `/list`, `/switch` commands)
- File upload (tell agent the file path directly)
- User login/authentication

### Tech

- Pure HTML + CSS + vanilla JS, no frameworks
- Inline lightweight Markdown: code blocks, bold, links, inline code
- All in one file, embedded at compile time

## Optional Interfaces Implemented

| Interface | Purpose |
|-----------|---------|
| `ReplyContextReconstructor` | Enables cron/relay to send messages to the WS client |
| `MessageUpdater` | Enables streaming preview (in-place message updates) |

### Not Implemented

- `TypingIndicator` — thinking events + connection status suffice
- `InlineButtonSender` — permission handled via custom JSON events
- `CardSender` — not applicable to web UI
- `CommandRegistrar` — browser has no command menu

## Architecture

```
Browser ──ws──▶ Hub ──▶ Platform.handler(msg) ──▶ Engine ──▶ Agent
                                                    │
Browser ◀──ws── Hub ◀── Platform.Reply/Send ◀───────┘
```

- `Hub` manages connected clients (register/unregister/broadcast)
- `Platform.Start()` launches HTTP server with two routes: `/` (static) and `/ws` (upgrade)
- Each WebSocket connection spawns a read pump goroutine
- Incoming JSON is decoded, validated, and dispatched to `Engine.handleMessage`
- Engine replies flow through `Platform.Reply/Send`, which serializes JSON and writes to the client via Hub
