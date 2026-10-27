package net

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

type transparentTestConn struct{ net.Conn }

func (c *transparentTestConn) NetConn() net.Conn { return c.Conn }

func TestBoundedTCPSocketsPreserveWrappedIO(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.Dial("tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client.SetDeadline(time.Now().Add(5 * time.Second))
	server.SetDeadline(time.Now().Add(5 * time.Second))
	wrapped := &transparentTestConn{Conn: &transparentTestConn{Conn: server}}
	if err := setTCPSocketBuffers(wrapped, 65536); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("bounded-concurrent-transfer"), 10000)
	done := make(chan error, 1)
	go func() { _, err := wrapped.Write(payload); done <- err }()
	received := make([]byte, len(payload))
	if _, err := io.ReadFull(client, received); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(received, payload) {
		t.Fatal("payload changed through bounded socket")
	}
}
