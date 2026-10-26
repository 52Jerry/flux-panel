package net

import (
	"io"
	"net"
	"testing"
	"time"
)

func TestTransportClosesBothSidesOnEOF(t *testing.T) {
	for _, peer := range []string{"client", "upstream"} {
		t.Run(peer, func(t *testing.T) {
			client, inbound := net.Pipe()
			outbound, upstream := net.Pipe()
			t.Cleanup(func() {
				client.Close()
				inbound.Close()
				outbound.Close()
				upstream.Close()
			})
			done := make(chan error, 1)
			go func() { done <- Transport(inbound, outbound) }()

			if peer == "client" {
				client.Close()
			} else {
				upstream.Close()
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				inbound.Close()
				outbound.Close()
				<-done
				t.Fatal("Transport kept the other side open after EOF")
			}
		})
	}
}

func TestTransportForwardsBothDirections(t *testing.T) {
	client, inbound := net.Pipe()
	outbound, upstream := net.Pipe()
	defer client.Close()
	defer inbound.Close()
	defer outbound.Close()
	defer upstream.Close()
	client.SetDeadline(time.Now().Add(2 * time.Second))
	upstream.SetDeadline(time.Now().Add(2 * time.Second))
	done := make(chan error, 1)
	go func() { done <- Transport(inbound, outbound) }()

	for _, pair := range [][2]net.Conn{{client, upstream}, {upstream, client}} {
		written := make(chan error, 1)
		go func(src net.Conn) {
			_, err := src.Write([]byte("payload"))
			written <- err
		}(pair[0])
		buf := make([]byte, len("payload"))
		if _, err := io.ReadFull(pair[1], buf); err != nil {
			t.Fatal(err)
		}
		if string(buf) != "payload" {
			t.Fatalf("received %q", buf)
		}
		if err := <-written; err != nil {
			t.Fatal(err)
		}
	}
	client.Close()
	upstream.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Transport did not finish")
	}
}

func TestTransportIdleTimeoutClosesBothSides(t *testing.T) {
	client, inbound := net.Pipe()
	outbound, upstream := net.Pipe()
	defer client.Close()
	defer inbound.Close()
	defer outbound.Close()
	defer upstream.Close()
	done := make(chan error, 1)
	go func() { done <- TransportWithIdleTimeout(inbound, outbound, 1) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an idle timeout error")
		}
	case <-time.After(3 * time.Second):
		inbound.Close()
		outbound.Close()
		<-done
		t.Fatal("idle transport did not release both sides")
	}
}

func TestTransportIdleTimeoutInterruptsBothBlockedWrites(t *testing.T) {
	client, inbound := net.Pipe()
	outbound, upstream := net.Pipe()
	defer client.Close()
	defer inbound.Close()
	defer outbound.Close()
	defer upstream.Close()
	done := make(chan error, 1)
	go func() { done <- TransportWithIdleTimeout(inbound, outbound, 1) }()
	// Each transport copy reads one payload, then both block trying to write
	// to a peer which never reads. A read-only deadline cannot release this.
	sent := make(chan error, 2)
	for _, peer := range []net.Conn{client, upstream} {
		go func(peer net.Conn) { _, err := peer.Write([]byte("payload")); sent <- err }(peer)
	}
	for i := 0; i < 2; i++ {
		select {
		case err := <-sent:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("transport did not read both payloads")
		}
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected write deadline error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("both copy workers remained blocked in Write")
	}
}
