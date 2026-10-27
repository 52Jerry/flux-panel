//go:build linux

package tcp

import (
	"context"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func socketAlgorithm(t *testing.T, c interface {
	SyscallConn() (syscall.RawConn, error)
}) string {
	t.Helper()
	raw, err := c.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var result string
	var socketErr error
	if err = raw.Control(func(fd uintptr) {
		result, socketErr = unix.GetsockoptString(int(fd), unix.IPPROTO_TCP, unix.TCP_CONGESTION)
	}); err != nil {
		t.Fatal(err)
	}
	if socketErr != nil {
		t.Fatal(socketErr)
	}
	return strings.TrimRight(result, "\x00")
}

func TestListenerCongestionInherited(t *testing.T) {
	lc := net.ListenConfig{}
	if err := configureCongestionControl(&lc, "reno"); err != nil {
		t.Fatal(err)
	}
	ln, err := lc.Listen(context.Background(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	tcpLn := ln.(*net.TCPListener)
	tcpLn.SetDeadline(time.Now().Add(3 * time.Second))
	c, err := net.DialTimeout("tcp4", ln.Addr().String(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	accepted, err := tcpLn.AcceptTCP()
	if err != nil {
		t.Fatal(err)
	}
	defer accepted.Close()
	if got := socketAlgorithm(t, accepted); got != "reno" {
		t.Fatalf("accepted algorithm %q", got)
	}
	if got := socketAlgorithm(t, tcpLn); got != "reno" {
		t.Fatalf("listener algorithm %q", got)
	}
}

func TestMissingAlgorithmDoesNotFallBack(t *testing.T) {
	lc := net.ListenConfig{}
	if err := configureCongestionControl(&lc, "no_such_cc"); err != nil {
		t.Fatal(err)
	}
	ln, err := lc.Listen(context.Background(), "tcp4", "127.0.0.1:0")
	if ln != nil {
		ln.Close()
		t.Fatal("unavailable algorithm silently opened a listener")
	}
	if err == nil {
		t.Fatal("unavailable algorithm was ignored")
	}
}
