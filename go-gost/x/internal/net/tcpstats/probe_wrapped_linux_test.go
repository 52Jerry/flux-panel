//go:build linux

package tcpstats_test

import (
	"crypto/tls"
	"net"
	"testing"

	"github.com/go-gost/x/internal/net/tcpstats"
	xstats "github.com/go-gost/x/observer/stats"
	statswrapper "github.com/go-gost/x/observer/stats/wrapper"
)

func TestWrappedTLSSocketCanBeSampledWithoutDecryptingTraffic(t *testing.T) {
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
	wrapped := statswrapper.WrapConn(tls.Client(client, &tls.Config{}), xstats.NewStats(false))
	defer wrapped.Close()
	release := tcpstats.Track(wrapped, nil)
	defer release()
	if sample := tcpstats.Snapshot(); sample.SampledConnections < 1 || sample.SampleErrors != 0 || sample.UnavailableConnections != 0 {
		t.Fatalf("wrapped TLS socket was not sampled: %+v", sample)
	}
}
