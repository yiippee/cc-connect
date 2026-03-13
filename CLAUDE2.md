# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

cc-connect is a message bridge connecting local AI coding agents (Claude Code, Cursor, Gemini CLI, Codex, etc.) to messaging platforms (Feishu, DingTalk, Telegram, Slack, Discord, LINE, WeChat Work, QQ). It uses long-polling/WebSocket connections so no public URL is needed for most platforms.

**Go 1.24.2 | Module: `github.com/chenhg5/cc-connect`**

## Build & Test Commands

```bash
make build          # Build binary with version/commit/time injected via ldflags
make test           # go test -v ./...
make lint           # golangci-lint run ./...
make run            # Build and run
make release TARGET=linux/amd64   # Cross-compile for specific platform
```

Run a single test:
```bash
go test -v -run TestEngine_BannedWords ./core/
```

Static binaries: `CGO_ENABLED=0` is used for release builds.

## Architecture

### Message Flow

```
User Message → Platform → MessageHandler → Engine → AgentSession → Events → Engine → Platform.Reply()
```

The `Engine` (`core/engine.go`) is the central router. It receives messages from platforms, manages agent sessions, processes events (text, tool use, permission requests, thinking), and sends replies back through platforms.

### Plugin Registry Pattern

Agents and platforms self-register via `init()` functions using a factory pattern:

```go
// Each agent/platform package registers itself
func init() { core.RegisterAgent("claudecode", New) }
func init() { core.RegisterPlatform("feishu", New) }
```

`cmd/cc-connect/main.go` blank-imports all agent/platform packages to trigger registration. Adding a new agent or platform only requires implementing the interface and adding a blank import.

### Core Interfaces (`core/interfaces.go`)

- **`Platform`** — Start, Reply, Send, Stop, Name
- **`Agent`** — StartSession, ListSessions, Stop, Name
- **`AgentSession`** — Send, RespondPermission, Events, Close, Alive

Many optional interfaces extend behavior without burdening all implementations:
`ProviderSwitcher`, `MemoryFileProvider`, `ModelSwitcher`, `TypingIndicator`, `MessageUpdater`, `ToolAuthorizer`, `ContextCompressor`, `CommandProvider`, `ModeSwitcher`, `SessionDeleter`, `InlineButtonSender`, `ReplyContextReconstructor`

### Key Components

| Package | Purpose |
|---------|---------|
| `cmd/cc-connect/` | CLI entry point, config loading, daemon management, subcommands |
| `core/` | Engine, session manager, command registry, i18n, speech/TTS, cron, relay, rate limiting, dedup, markdown conversion |
| `agent/` | Agent adapters (claudecode, cursor, gemini, codex, qoder, opencode, iflow) |
| `platform/` | Platform adapters (feishu, dingtalk, telegram, slack, discord, line, wecom, qq, qqbot) |
| `config/` | TOML config parsing and hot-reload |
| `daemon/` | systemd/launchd service management |

### Session Management (`core/session.go`)

Sessions are identified by `sessionKey` (format: `{platform}:{chatID}:{userID}`). Each user can have multiple sessions with one active at a time. Sessions persist to JSON files in `data_dir`. Session locking (`busy` flag + mutex) prevents concurrent message processing.

### Concurrency Model

- Message processing runs in goroutines (handler returns immediately)
- `sync.Mutex` for session state, `sync.RWMutex` for read-heavy data (commands, aliases)
- Agent interactions spawn goroutines; event channels stay open across turns
- TTS replies generated async

## Configuration

TOML format. Loaded from (in order): `-config` flag → `./config.toml` → `~/.cc-connect/config.toml`. See `config.example.toml` (embedded in binary via `embed.go`) for full reference.

Multi-project: a single process manages multiple `[[projects]]`, each with its own agent + platforms.

## Testing Patterns

- Stub implementations (`stubAgent`, `stubAgentSession`, `stubPlatformEngine`) in test files
- Table-driven tests throughout
- Standard `testing` package only, no external test framework

## Event Types (`core/message.go`)

`EventText`, `EventToolUse`, `EventToolResult`, `EventResult`, `EventError`, `EventPermissionRequest`, `EventThinking` — these flow from agent sessions through the engine to platforms.

## Adding a New Agent or Platform

1. Create a package under `agent/` or `platform/`
2. Implement the `Agent`/`Platform` interface (plus optional interfaces as needed)
3. Register via `init()` with `core.RegisterAgent()` or `core.RegisterPlatform()`
4. Add blank import in `cmd/cc-connect/main.go`
