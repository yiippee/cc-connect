# WebSocket Platform Adapter Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a generic WebSocket platform to cc-connect so users can interact with agents via a browser-based chat UI at `http://localhost:9000`.

**Architecture:** A new `platform/ws/` package registers as platform type `"ws"`. It starts an HTTP server with two routes: `/` serves an embedded `index.html` chat UI, `/ws` upgrades to WebSocket. A `Hub` manages connected clients. JSON messages flow bidirectionally. The platform implements `ReplyContextReconstructor` and `MessageUpdater` optional interfaces.

**Tech Stack:** Go, gorilla/websocket (already in go.mod), `//go:embed` for static assets, vanilla HTML/CSS/JS for the chat UI.

**Spec:** `docs/superpowers/specs/2026-03-14-ws-platform-design.md`

---

## File Structure

| File | Responsibility |
|------|----------------|
| `platform/ws/ws.go` | Platform struct, factory, Start/Stop/Reply/Send, HTTP server, WebSocket handler, ReplyContextReconstructor, MessageUpdater |
| `platform/ws/hub.go` | Hub struct: register/unregister clients, broadcast JSON to all connected clients |
| `platform/ws/static/index.html` | Embedded chat UI: message list, input, Markdown rendering, permission cards, reconnect logic |
| `platform/ws/embed.go` | `//go:embed static/index.html` directive |
| `platform/ws/ws_test.go` | Unit tests for Platform (WebSocket dial, send/receive JSON, Reply, Send, UpdateMessage, ReconstructReplyCtx) |
| `cmd/cc-connect/plugin_platform_ws.go` | Build-tag plugin file: `//go:build !no_ws` with blank import |

---

## Chunk 1: Core Implementation

### Task 1: Hub — connection manager

**Files:**
- Create: `platform/ws/hub.go`

- [ ] **Step 1: Create `platform/ws/hub.go`**

```go
package ws

import (
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/gorilla/websocket"
)

// client wraps a single WebSocket connection.
type client struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

// writeJSON sends a JSON message to this client (thread-safe).
func (c *client) writeJSON(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteJSON(v)
}

// hub manages all connected WebSocket clients.
type hub struct {
	mu      sync.RWMutex
	clients map[*client]struct{}
}

func newHub() *hub {
	return &hub{clients: make(map[*client]struct{})}
}

func (h *hub) register(c *client) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	slog.Info("ws: client connected", "total", h.len())
}

func (h *hub) unregister(c *client) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
	slog.Info("ws: client disconnected", "total", h.len())
}

func (h *hub) len() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// outMessage is the JSON envelope sent to clients.
type outMessage struct {
	Type      string `json:"type"`
	Content   string `json:"content"`
	RequestID string `json:"request_id,omitempty"`
	MessageID string `json:"message_id,omitempty"`
}

// broadcast sends a JSON message to all connected clients.
func (h *hub) broadcast(msg outMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		slog.Error("ws: marshal broadcast", "error", err)
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		c.mu.Lock()
		err := c.conn.WriteMessage(websocket.TextMessage, data)
		c.mu.Unlock()
		if err != nil {
			slog.Debug("ws: broadcast write error", "error", err)
		}
	}
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd D:/github/yiippee/cc-connect && go build ./platform/ws/`
Expected: Compile error (no Go files yet referencing this package — that's fine, just ensure no syntax errors by running `go vet ./platform/ws/`)

---

### Task 2: Platform — WebSocket server and core.Platform implementation

**Files:**
- Create: `platform/ws/ws.go`
- Create: `platform/ws/embed.go`

- [ ] **Step 1: Create `platform/ws/embed.go`**

```go
package ws

import "embed"

//go:embed static/index.html
var staticFS embed.FS
```

- [ ] **Step 2: Create placeholder `platform/ws/static/index.html`**

Minimal placeholder so embed works:

```html
<!DOCTYPE html>
<html><head><title>cc-connect</title></head>
<body><p>loading...</p></body></html>
```

- [ ] **Step 3: Create `platform/ws/ws.go`**

```go
package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

func init() {
	core.RegisterPlatform("ws", New)
}

// replyContext is the opaque context passed through core.Message.ReplyCtx.
type replyContext struct {
	sessionKey string
}

// previewHandle identifies a streaming preview message for UpdateMessage.
type previewHandle struct {
	id string // monotonic ID
}

// Platform implements core.Platform for generic WebSocket connections.
type Platform struct {
	addr      string
	allowFrom string
	handler   core.MessageHandler
	hub       *hub
	server    *http.Server
	listener  net.Listener

	mu        sync.Mutex
	previewID int // monotonic counter for preview handles
}

func New(opts map[string]any) (core.Platform, error) {
	addr, _ := opts["addr"].(string)
	if addr == "" {
		addr = "127.0.0.1:9000"
	}
	allowFrom, _ := opts["allow_from"].(string)
	core.CheckAllowFrom("ws", allowFrom)

	return &Platform{
		addr:      addr,
		allowFrom: allowFrom,
		hub:       newHub(),
	}, nil
}

func (p *Platform) Name() string { return "ws" }

func (p *Platform) Start(handler core.MessageHandler) error {
	p.handler = handler

	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	mux := http.NewServeMux()

	// Serve embedded static files at /
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return fmt.Errorf("ws: embed sub: %w", err)
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))

	// WebSocket endpoint
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			slog.Error("ws: upgrade failed", "error", err)
			return
		}
		c := &client{conn: conn}
		p.hub.register(c)
		defer func() {
			p.hub.unregister(c)
			conn.Close()
		}()

		// Configure pong handler for heartbeat
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		conn.SetPongHandler(func(string) error {
			conn.SetReadDeadline(time.Now().Add(60 * time.Second))
			return nil
		})

		// Start ping ticker
		done := make(chan struct{})
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					c.mu.Lock()
					err := conn.WriteMessage(websocket.PingMessage, nil)
					c.mu.Unlock()
					if err != nil {
						return
					}
				}
			}
		}()
		defer close(done)

		// Read pump
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
					slog.Debug("ws: read error", "error", err)
				}
				return
			}
			conn.SetReadDeadline(time.Now().Add(60 * time.Second))

			var incoming struct {
				Type      string `json:"type"`
				Content   string `json:"content"`
				RequestID string `json:"request_id"`
				Allow     *bool  `json:"allow"`
			}
			if err := json.Unmarshal(raw, &incoming); err != nil {
				slog.Debug("ws: invalid json", "error", err)
				continue
			}

			sessionKey := "ws:default"

			switch incoming.Type {
			case "message":
				if incoming.Content == "" {
					continue
				}
				msg := &core.Message{
					SessionKey: sessionKey,
					Platform:   "ws",
					UserID:     "local",
					UserName:   "user",
					Content:    incoming.Content,
					MessageID:  fmt.Sprintf("ws-%d", time.Now().UnixNano()),
					ReplyCtx:   replyContext{sessionKey: sessionKey},
				}
				p.handler(p, msg)

			case "permission_response":
				if incoming.RequestID == "" || incoming.Allow == nil {
					continue
				}
				behavior := "deny"
				if *incoming.Allow {
					behavior = "allow"
				}
				// Deliver as a regular message — the engine's permission
				// handler matches on pending permission state, not message content.
				msg := &core.Message{
					SessionKey: sessionKey,
					Platform:   "ws",
					UserID:     "local",
					UserName:   "user",
					Content:    behavior,
					MessageID:  fmt.Sprintf("ws-%d", time.Now().UnixNano()),
					ReplyCtx:   replyContext{sessionKey: sessionKey},
				}
				p.handler(p, msg)
			}
		}
	})

	ln, err := net.Listen("tcp", p.addr)
	if err != nil {
		return fmt.Errorf("ws: listen %s: %w", p.addr, err)
	}
	p.listener = ln
	p.server = &http.Server{Handler: mux}

	go func() {
		slog.Info("ws: serving", "addr", p.addr)
		if err := p.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("ws: server error", "error", err)
		}
	}()

	return nil
}

func (p *Platform) Reply(_ context.Context, rctx any, content string) error {
	p.hub.broadcast(outMessage{Type: "reply", Content: content})
	return nil
}

func (p *Platform) Send(_ context.Context, rctx any, content string) error {
	p.hub.broadcast(outMessage{Type: "reply", Content: content})
	return nil
}

func (p *Platform) Stop() error {
	if p.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return p.server.Shutdown(ctx)
	}
	return nil
}

// --- Optional interfaces ---

// ReconstructReplyCtx implements core.ReplyContextReconstructor.
func (p *Platform) ReconstructReplyCtx(sessionKey string) (any, error) {
	return replyContext{sessionKey: sessionKey}, nil
}

// SendPreviewStart implements core.PreviewStarter.
func (p *Platform) SendPreviewStart(_ context.Context, _ any, content string) (any, error) {
	p.mu.Lock()
	p.previewID++
	id := fmt.Sprintf("preview-%d", p.previewID)
	p.mu.Unlock()

	p.hub.broadcast(outMessage{Type: "reply", Content: content, MessageID: id})
	return &previewHandle{id: id}, nil
}

// UpdateMessage implements core.MessageUpdater.
func (p *Platform) UpdateMessage(_ context.Context, handle any, content string) error {
	h, ok := handle.(*previewHandle)
	if !ok {
		return fmt.Errorf("ws: invalid preview handle type %T", handle)
	}
	p.hub.broadcast(outMessage{Type: "update", Content: content, MessageID: h.id})
	return nil
}
```

- [ ] **Step 4: Verify compilation**

Run: `cd D:/github/yiippee/cc-connect && go build ./platform/ws/`
Expected: SUCCESS

---

### Task 3: Plugin registration file

**Files:**
- Create: `cmd/cc-connect/plugin_platform_ws.go`

- [ ] **Step 1: Create the plugin file**

```go
//go:build !no_ws

package main

import _ "github.com/chenhg5/cc-connect/platform/ws"
```

- [ ] **Step 2: Verify full project compiles**

Run: `cd D:/github/yiippee/cc-connect && go build ./cmd/cc-connect/`
Expected: SUCCESS

- [ ] **Step 3: Commit core platform implementation**

```bash
cd D:/github/yiippee/cc-connect
git add platform/ws/ cmd/cc-connect/plugin_platform_ws.go
git commit -m "feat: add WebSocket platform adapter with hub and plugin registration"
```

---

## Chunk 2: Chat UI

### Task 4: Embedded chat UI

**Files:**
- Modify: `platform/ws/static/index.html`

- [ ] **Step 1: Replace placeholder with full chat UI**

The `index.html` should implement:

**Layout:**
- Full-height dark theme page
- Top bar with connection status indicator (green dot = connected, red = disconnected)
- Scrollable message area in the center
- Fixed input area at bottom: textarea + send button

**Message rendering:**
- User messages: right-aligned, distinct background color
- AI replies: left-aligned, with basic Markdown rendering:
  - Code blocks (``` fenced) with monospace background
  - Inline code with backtick styling
  - Bold (`**text**`) and italic (`*text*`)
  - Links (`[text](url)`) as clickable `<a>` tags
  - Line breaks preserved
- Thinking events: collapsible section with dimmed styling, default collapsed
- Tool use events: collapsible section with dimmed styling, default collapsed
- Permission requests: card with description text + Allow (green) / Deny (red) buttons

**WebSocket logic (vanilla JS):**
- Connect to `ws://${location.host}/ws`
- On open: set status to "connected" (green)
- On close: set status to "disconnected" (red), auto-retry every 3 seconds
- On message: parse JSON, dispatch by `type`:
  - `reply`: append new message bubble (or update existing if `message_id` matches)
  - `update`: find message by `message_id`, replace content
  - `thinking`: append collapsible thinking section
  - `tool_use`: append collapsible tool section
  - `permission`: append permission card with buttons
- Send: on Enter (without Shift), serialize `{"type":"message","content":"..."}` and send
- Permission buttons: serialize `{"type":"permission_response","request_id":"...","allow":true/false}`

**Styling (CSS in `<style>`):**
- Dark background (#1a1a2e or similar), light text
- Monospace font for code, system font for text
- Responsive: works on both desktop and mobile browsers
- Smooth scroll behavior
- Input textarea auto-grows up to 5 lines

- [ ] **Step 2: Verify it compiles with the new HTML**

Run: `cd D:/github/yiippee/cc-connect && go build ./platform/ws/`
Expected: SUCCESS

- [ ] **Step 3: Commit chat UI**

```bash
cd D:/github/yiippee/cc-connect
git add platform/ws/static/index.html
git commit -m "feat: add embedded chat UI for WebSocket platform"
```

---

## Chunk 3: Tests

### Task 5: Unit tests

**Files:**
- Create: `platform/ws/ws_test.go`

- [ ] **Step 1: Write tests**

Test cases to cover:
1. `TestNew_Defaults` — verify default addr is `127.0.0.1:9000`
2. `TestNew_CustomAddr` — verify custom addr from options
3. `TestNew_MissingAddr` — verify default used when addr empty
4. `TestStartStop` — Start the platform, verify HTTP server is listening, Stop it
5. `TestWebSocket_SendReceive` — Start platform, dial WebSocket, send a `message` JSON, verify handler receives `core.Message` with correct fields
6. `TestWebSocket_Reply` — Start platform, connect client, call `platform.Reply()`, verify client receives JSON with `type:"reply"`
7. `TestWebSocket_UpdateMessage` — Call `SendPreviewStart`, then `UpdateMessage`, verify client receives `type:"update"` with matching `message_id`
8. `TestReconstructReplyCtx` — verify returns valid replyContext for any session key
9. `TestStaticFileServing` — HTTP GET `/` returns HTML content

Each test should:
- Use a random port (`127.0.0.1:0`) to avoid conflicts
- Clean up with `defer platform.Stop()`
- Use `gorilla/websocket.Dial` for WebSocket tests

- [ ] **Step 2: Run tests**

Run: `cd D:/github/yiippee/cc-connect && go test ./platform/ws/ -v`
Expected: All tests PASS

- [ ] **Step 3: Commit tests**

```bash
cd D:/github/yiippee/cc-connect
git add platform/ws/ws_test.go
git commit -m "test: add unit tests for WebSocket platform adapter"
```

---

## Chunk 4: Integration verification

### Task 6: End-to-end verification

- [ ] **Step 1: Verify full build**

Run: `cd D:/github/yiippee/cc-connect && go build -o /dev/null ./cmd/cc-connect/`
Expected: SUCCESS

- [ ] **Step 2: Verify selective compilation with `no_ws` tag**

Run: `cd D:/github/yiippee/cc-connect && go build -tags no_ws -o /dev/null ./cmd/cc-connect/`
Expected: SUCCESS (ws platform excluded)

- [ ] **Step 3: Run all project tests to check for regressions**

Run: `cd D:/github/yiippee/cc-connect && go test ./...`
Expected: All tests PASS

- [ ] **Step 4: Final commit if any fixes were needed**

Only if changes were made during verification.
