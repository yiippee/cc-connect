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
	id string
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
	previewID int
}

// New creates a new WebSocket platform from config options.
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

// Addr returns the listener address. Useful in tests with port 0.
func (p *Platform) Addr() string {
	if p.listener != nil {
		return p.listener.Addr().String()
	}
	return p.addr
}

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
		slog.Info("ws: serving", "addr", ln.Addr().String())
		if err := p.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("ws: server error", "error", err)
		}
	}()

	return nil
}

func (p *Platform) Reply(_ context.Context, _ any, content string) error {
	p.hub.broadcast(outMessage{Type: "reply", Content: content})
	return nil
}

func (p *Platform) Send(_ context.Context, _ any, content string) error {
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
