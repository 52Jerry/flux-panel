//go:build linux

package tcpstats

import (
	"errors"
	"net"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

func TestLinuxProbeReadsAnActualKernelTCPSocket(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, e := ln.Accept()
		if e == nil {
			accepted <- c
		}
	}()
	client, err := net.Dial("tcp4", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server := <-accepted
	defer server.Close()
	info, err := probeConn(client)
	if err != nil {
		t.Fatal(err)
	}
	if info.congestionControl == "" {
		t.Fatal("kernel congestion algorithm missing")
	}
	if _, err = probeConn(&testConn{1}); !errors.Is(err, errUnavailable) {
		t.Fatalf("fake socket was treated as kernel TCP: %v", err)
	}
}

func TestShortTCPInfoOmitsUnavailableFields(t *testing.T) {
	info := unix.TCPInfo{Rtt: 12000, Total_retrans: 4, Notsent_bytes: 100, Delivery_rate: 9000}
	short := decodeInfo(&info, uint32(unsafe.Offsetof(info.Notsent_bytes)))
	if short.rttMicros != 12000 || !short.hasRetransmissions || short.hasNotsentBytes || short.notsentBytes != 0 || short.deliveryBytesPerSecond != 0 {
		t.Fatalf("short TCP_INFO treated tail as available: %+v", short)
	}
	full := decodeInfo(&info, uint32(unsafe.Sizeof(info)))
	if !full.hasNotsentBytes || full.notsentBytes != 100 || full.deliveryBytesPerSecond != 9000 {
		t.Fatalf("full TCP_INFO decoding failed: %+v", full)
	}
}
