package cmd

import (
	"strings"
	"testing"

	_ "github.com/go-gost/x/connector/wg"
	_ "github.com/go-gost/x/dialer/wg"
	"github.com/go-gost/x/registry"
)

func TestWireGuardCLI(t *testing.T) {
	for _, scheme := range []string{"wg", "wireguard", "wg+wg"} {
		u, err := Norm(scheme + "://127.0.0.1:51820?address=10.0.0.2%2F32&allowedIPs=0.0.0.0%2F0")
		if err != nil {
			t.Fatal(err)
		}
		n, err := buildNodeConfig(u)
		if err != nil {
			t.Fatal(err)
		}
		if n.Connector.Type == "http" || n.Dialer.Type == "tcp" {
			t.Fatal("WireGuard silently fell back to plaintext")
		}
		if n.Dialer.Metadata["allowedIPs"] != "0.0.0.0/0" {
			t.Fatal("WireGuard metadata lost")
		}
	}
	for _, scheme := range []string{"http+wg", "wg+tcp"} {
		u, _ := Norm(scheme + "://127.0.0.1:51820")
		if _, err := buildNodeConfig(u); err == nil {
			t.Fatal("incompatible WireGuard pair accepted")
		}
	}
}

func TestWireGuardCLIMissingRegistration(t *testing.T) {
	r := registry.DialerRegistry()
	factory := r.Get("wg")
	r.Unregister("wg")
	defer r.Register("wg", factory)
	u, _ := Norm("wg://127.0.0.1:51820")
	if _, err := buildNodeConfig(u); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("expected explicit missing-support error, got %v", err)
	}
}
