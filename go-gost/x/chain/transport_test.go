package chain

import (
	"testing"

	"github.com/go-gost/core/chain"
)

func TestTransportCopyDoesNotMutateOriginalRoute(t *testing.T) {
	original := NewTransport(nil, nil, chain.AddrTransportOption("127.0.0.1:51820"))
	copy := original.Copy()
	copy.Options().Route = NewRoute()
	copy.Options().Addr = "127.0.0.1:51821"
	if original.Options().Route != nil || original.Options().Addr != "127.0.0.1:51820" {
		t.Fatal("route selection mutated the shared original transport")
	}
}
