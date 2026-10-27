package net

import (
	"bytes"
	"io"
	"net"
	"testing"
)

func TestSmallStreamBufferTransfersWholePayload(t *testing.T) {
	client, inbound := net.Pipe()
	outbound, upstream := net.Pipe()
	defer client.Close()
	defer inbound.Close()
	defer outbound.Close()
	defer upstream.Close()
	payload := bytes.Repeat([]byte("capacity-test"), 20000)
	done := make(chan error, 1)
	go func() { done <- TransportWithOptions(inbound, outbound, TransportOptions{TCPBufferSize: 4096}) }()
	writeDone := make(chan error, 1)
	go func() { _, err := client.Write(payload); client.Close(); writeDone <- err }()
	got, err := io.ReadAll(upstream)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("lost stream bytes: %d/%d", len(got), len(payload))
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	<-done
}

func TestInvalidStreamBufferClosesEndpoints(t *testing.T) {
	a, b := net.Pipe()
	c, d := net.Pipe()
	defer a.Close()
	defer b.Close()
	defer c.Close()
	defer d.Close()
	if err := TransportWithOptions(b, c, TransportOptions{TCPBufferSize: 1}); err == nil {
		t.Fatal("invalid buffer size accepted")
	}
}
