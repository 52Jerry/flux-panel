package socket

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func socketPair(t *testing.T) (*websocket.Conn, *websocket.Conn) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(rw, r, nil)
		if err == nil {
			accepted <- conn
		}
	}))
	t.Cleanup(server.Close)
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	peer := <-accepted
	t.Cleanup(func() { client.Close(); peer.Close() })
	return client, peer
}

func testReporter(t *testing.T) (*WebSocketReporter, *reporterSession, *websocket.Conn) {
	t.Helper()
	conn, peer := socketPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := &WebSocketReporter{conn: conn, connected: true, ctx: ctx, cancel: cancel, pingSlots: make(chan struct{}, maxConcurrentPings), commandSlot: make(chan struct{}, 1)}
	session := &reporterSession{conn: conn, ctx: ctx, cancel: cancel}
	return w, session, peer
}

func TestCancelledSessionLeavesControlCommandQueue(t *testing.T) {
	w, session, _ := testReporter(t)
	w.commandSlot <- struct{}{}
	done := make(chan struct{})
	go func() { defer close(done); w.routeCommand(session, CommandMessage{Type: "queued-command"}) }()
	session.cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("old session remained queued behind configuration command")
	}
	<-w.commandSlot
}

func pingTarget(t *testing.T) (string, int) {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

func waitForPingRelease(t *testing.T, w *WebSocketReporter) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for len(w.pingSlots) != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if len(w.pingSlots) != 0 {
		t.Fatal("cancelled diagnostic workers retained their slots")
	}
}

func TestTcpPingDoesNotBlockControlReader(t *testing.T) {
	w, session, peer := testReporter(t)
	ip, port := pingTarget(t)
	readerDone := make(chan struct{})
	go func() { defer close(readerDone); w.receiveMessages(session) }()
	cmd := CommandMessage{Type: "TcpPing", RequestId: "ping", Data: TcpPingRequest{IP: ip, Port: port, Count: maxPingCount, Timeout: 100}}
	if err := peer.WriteJSON(cmd); err != nil {
		t.Fatal(err)
	}
	// A malformed control frame gets an immediate parse response. Previously
	// it waited behind all ten diagnostic attempts and their 100ms gaps.
	start := time.Now()
	if err := peer.WriteMessage(websocket.TextMessage, []byte("invalid-json")); err != nil {
		t.Fatal(err)
	}
	peer.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	var response CommandResponse
	if err := peer.ReadJSON(&response); err != nil {
		t.Fatal(err)
	}
	if response.Type != "ParseError" || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("control reader blocked: %+v", response)
	}
	session.cancel()
	session.conn.Close()
	waitForPingRelease(t, w)
	select {
	case <-readerDone:
	case <-time.After(time.Second):
		t.Fatal("reader did not stop")
	}
}

func TestTcpPingConcurrencyIsBoundedAndCancelled(t *testing.T) {
	w, session, peer := testReporter(t)
	ip, port := pingTarget(t)
	cmd := CommandMessage{Type: "TcpPing", Data: TcpPingRequest{IP: ip, Port: port, Count: maxPingCount, Timeout: 100}}
	for i := 0; i < maxConcurrentPings; i++ {
		w.startTcpPing(session, cmd)
	}
	cmd.RequestId = "overflow"
	w.startTcpPing(session, cmd)
	peer.SetReadDeadline(time.Now().Add(time.Second))
	var response CommandResponse
	if err := peer.ReadJSON(&response); err != nil {
		t.Fatal(err)
	}
	if response.RequestId != "overflow" || response.Success {
		t.Fatalf("unbounded diagnostic admission: %+v", response)
	}
	if len(w.pingSlots) > maxConcurrentPings {
		t.Fatal("diagnostic worker limit exceeded")
	}
	session.cancel()
	waitForPingRelease(t, w)
}

func TestOldSessionCannotWriteToOrDisconnectReplacement(t *testing.T) {
	w, oldSession, _ := testReporter(t)
	newConn, newPeer := socketPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	newSession := &reporterSession{conn: newConn, ctx: ctx, cancel: cancel}
	w.connMutex.Lock()
	w.conn = newConn
	w.connected = true
	w.connMutex.Unlock()
	w.sendResponse(oldSession, CommandResponse{Type: "stale"})
	w.disconnect(oldSession)
	w.sendResponse(newSession, CommandResponse{Type: "current"})
	newPeer.SetReadDeadline(time.Now().Add(time.Second))
	_, message, err := newPeer.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var response CommandResponse
	if err := json.Unmarshal(message, &response); err != nil {
		t.Fatal(err)
	}
	if response.Type != "current" {
		t.Fatalf("stale session affected replacement: %s", message)
	}
}

func TestTcpPingBoundsAndContext(t *testing.T) {
	w := &WebSocketReporter{}
	for _, req := range []TcpPingRequest{
		{IP: "127.0.0.1", Port: 1, Count: maxPingCount + 1, Timeout: 100},
		{IP: "127.0.0.1", Port: 1, Count: 1, Timeout: maxPingTimeoutMS + 1},
	} {
		if _, err := w.handleTcpPing(context.Background(), req); err == nil {
			t.Fatal("oversized diagnostic accepted")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := tcpPingHost(ctx, "127.0.0.1", 1, 10, 5000)
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("diagnostic ignored total context: %v", err)
	}
}
