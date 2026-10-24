package wg

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Run against a freshly built GOST binary, not a helper that bypasses main,
// service registration, configuration loading, listeners or forward handlers.
func TestWireGuardStandaloneBinary(t *testing.T) {
	binary := os.Getenv("GOST_TEST_BINARY")
	if binary == "" {
		t.Skip("set GOST_TEST_BINARY to run the standalone executable integration test")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	p := newPeer(t)
	tcpTarget, udpTarget := p.echo(t, "tcp4"), p.echo(t, "udp4")
	tcpPort, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tcpLocal := tcpPort.Addr().String()
	tcpPort.Close()
	udpPort, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	udpLocal := udpPort.LocalAddr().String()
	udpPort.Close()
	service := func(name, network, local, remote string) map[string]any {
		return map[string]any{
			"name": name, "addr": local,
			"listener":  map[string]any{"type": network},
			"handler":   map[string]any{"type": network, "chain": "wireguard-out"},
			"forwarder": map[string]any{"nodes": []any{map[string]any{"name": "echo", "addr": remote}}},
		}
	}
	cfg := map[string]any{
		"services": []any{service("tcp-forward", "tcp", tcpLocal, tcpTarget), service("udp-forward", "udp", udpLocal, udpTarget)},
		"chains": []any{map[string]any{"name": "wireguard-out", "hops": []any{map[string]any{
			"name": "wg-hop", "nodes": []any{map[string]any{
				"name": "wg-peer", "addr": p.endpoint,
				"connector": map[string]any{"type": "wg"},
				"dialer":    map[string]any{"type": "wg", "metadata": p.metadata},
			}},
		}}}},
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	configPath := filepath.Join(dir, "gost-test.json")
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	version := exec.CommandContext(ctx, binary, "-V")
	version.Dir = dir // Deliberately no panel config.json in this directory.
	output, err := version.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "features=wireguard") {
		t.Fatalf("version command failed without panel config: %v", err)
	}
	cmd := exec.CommandContext(ctx, binary, "-standalone", "-C", configPath)
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); cmd.Wait() })
	var tcp net.Conn
	for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); {
		tcp, err = net.DialTimeout("tcp4", tcpLocal, 100*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("standalone TCP listener did not start: %v", err)
	}
	defer tcp.Close()
	if err := roundTrip(tcp, []byte("standalone-tcp-through-wireguard")); err != nil {
		t.Fatal(err)
	}
	udp, err := net.Dial("udp4", udpLocal)
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	if err := roundTrip(udp, []byte("standalone-udp-through-wireguard")); err != nil {
		t.Fatal(err)
	}
}
