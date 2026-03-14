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
