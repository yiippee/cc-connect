package ws

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

// startTestPlatform creates and starts a Platform on a random port.
func startTestPlatform(t *testing.T, handler core.MessageHandler) *Platform {
	t.Helper()
	p, err := New(map[string]any{"addr": "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	plat := p.(*Platform)
	if err := plat.Start(handler); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { plat.Stop() })
	return plat
}

// dialWS connects a WebSocket client to the test platform.
func dialWS(t *testing.T, p *Platform) *websocket.Conn {
	t.Helper()
	url := "ws://" + p.Addr() + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	t.Cleanup(func() { conn.Close() })
	// Give the server a moment to register the client
	time.Sleep(50 * time.Millisecond)
	return conn
}

func TestNew_Defaults(t *testing.T) {
	p, err := New(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	plat := p.(*Platform)
	if plat.addr != "127.0.0.1:9000" {
		t.Errorf("default addr = %q, want 127.0.0.1:9000", plat.addr)
	}
}

func TestNew_CustomAddr(t *testing.T) {
	p, err := New(map[string]any{"addr": "0.0.0.0:8080"})
	if err != nil {
		t.Fatal(err)
	}
	plat := p.(*Platform)
	if plat.addr != "0.0.0.0:8080" {
		t.Errorf("addr = %q, want 0.0.0.0:8080", plat.addr)
	}
}

func TestStartStop(t *testing.T) {
	noop := func(p core.Platform, msg *core.Message) {}
	plat := startTestPlatform(t, noop)

	// Verify HTTP server is listening
	resp, err := http.Get("http://" + plat.Addr() + "/")
	if err != nil {
		t.Fatalf("http get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestStaticFileServing(t *testing.T) {
	noop := func(p core.Platform, msg *core.Message) {}
	plat := startTestPlatform(t, noop)

	resp, err := http.Get("http://" + plat.Addr() + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])
	if !strings.Contains(body, "cc-connect") {
		t.Error("response body should contain 'cc-connect'")
	}
}

func TestWebSocket_SendReceive(t *testing.T) {
	var received *core.Message
	var mu sync.Mutex
	done := make(chan struct{})

	handler := func(p core.Platform, msg *core.Message) {
		mu.Lock()
		received = msg
		mu.Unlock()
		close(done)
	}

	plat := startTestPlatform(t, handler)
	conn := dialWS(t, plat)

	// Send a message
	msg := map[string]string{"type": "message", "content": "hello world"}
	if err := conn.WriteJSON(msg); err != nil {
		t.Fatal(err)
	}

	// Wait for handler
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for message")
	}

	mu.Lock()
	defer mu.Unlock()

	if received == nil {
		t.Fatal("no message received")
	}
	if received.Content != "hello world" {
		t.Errorf("content = %q, want 'hello world'", received.Content)
	}
	if received.Platform != "ws" {
		t.Errorf("platform = %q, want 'ws'", received.Platform)
	}
	if received.SessionKey != "ws:default" {
		t.Errorf("sessionKey = %q, want 'ws:default'", received.SessionKey)
	}
	if received.UserID != "local" {
		t.Errorf("userID = %q, want 'local'", received.UserID)
	}
}

func TestWebSocket_Reply(t *testing.T) {
	noop := func(p core.Platform, msg *core.Message) {}
	plat := startTestPlatform(t, noop)
	conn := dialWS(t, plat)

	// Send reply from platform
	if err := plat.Reply(context.Background(), replyContext{sessionKey: "ws:default"}, "hi there"); err != nil {
		t.Fatal(err)
	}

	// Read from WebSocket
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}

	var out outMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Type != "reply" {
		t.Errorf("type = %q, want 'reply'", out.Type)
	}
	if out.Content != "hi there" {
		t.Errorf("content = %q, want 'hi there'", out.Content)
	}
}

func TestWebSocket_UpdateMessage(t *testing.T) {
	noop := func(p core.Platform, msg *core.Message) {}
	plat := startTestPlatform(t, noop)
	conn := dialWS(t, plat)

	// SendPreviewStart
	handle, err := plat.SendPreviewStart(context.Background(), nil, "streaming...")
	if err != nil {
		t.Fatal(err)
	}

	// Read initial message
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var initial outMessage
	json.Unmarshal(raw, &initial)
	if initial.Type != "reply" || initial.MessageID == "" {
		t.Errorf("initial: type=%q messageId=%q", initial.Type, initial.MessageID)
	}

	// UpdateMessage
	if err := plat.UpdateMessage(context.Background(), handle, "updated content"); err != nil {
		t.Fatal(err)
	}

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, raw, err = conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var update outMessage
	json.Unmarshal(raw, &update)
	if update.Type != "update" {
		t.Errorf("update type = %q, want 'update'", update.Type)
	}
	if update.Content != "updated content" {
		t.Errorf("update content = %q, want 'updated content'", update.Content)
	}
	if update.MessageID != initial.MessageID {
		t.Errorf("messageId mismatch: %q != %q", update.MessageID, initial.MessageID)
	}
}

func TestReconstructReplyCtx(t *testing.T) {
	p, _ := New(map[string]any{})
	plat := p.(*Platform)

	ctx, err := plat.ReconstructReplyCtx("ws:default")
	if err != nil {
		t.Fatal(err)
	}
	rc, ok := ctx.(replyContext)
	if !ok {
		t.Fatalf("type = %T, want replyContext", ctx)
	}
	if rc.sessionKey != "ws:default" {
		t.Errorf("sessionKey = %q, want 'ws:default'", rc.sessionKey)
	}
}

func TestPlatformName(t *testing.T) {
	p, _ := New(map[string]any{})
	if p.Name() != "ws" {
		t.Errorf("Name() = %q, want 'ws'", p.Name())
	}
}
