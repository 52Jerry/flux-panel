package main

import (
	"testing"

	"github.com/go-gost/x/registry"
)

func TestWireGuardRegisteredInBinary(t *testing.T) {
	for _, name := range []string{"wg", "wireguard"} {
		if registry.DialerRegistry().Get(name) == nil || registry.ConnectorRegistry().Get(name) == nil {
			t.Fatalf("%s is missing from binary registrations", name)
		}
	}
}
