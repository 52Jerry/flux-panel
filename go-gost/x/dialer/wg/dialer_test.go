package wg

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gost/core/dialer"
	corelogger "github.com/go-gost/core/logger"
	wgconnector "github.com/go-gost/x/connector/wg"
	"github.com/go-gost/x/logger"
	md "github.com/go-gost/x/metadata"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

func TestMain(m *testing.M) {
	// The application's main initializes the default logger before parsing routes.
	corelogger.SetDefault(logger.NewLogger(logger.OutputOption(io.Discard)))
	os.Exit(m.Run())
}

func newKey(t *testing.T) *ecdh.PrivateKey {
	t.Helper()
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

type testPeer struct {
	dev      *device.Device
	stack    *netstack.Net
	endpoint string
	metadata map[string]any
}

// This is an independent wireguard-go peer using its real OS UDP bind, not a
// mocked encryption/handshake. Both IPv4 and IPv6 exist only inside the tunnel.
func newPeer(t *testing.T) *testPeer {
	t.Helper()
	client, server := newKey(t), newKey(t)
	psk := make([]byte, 32)
	if _, err := rand.Read(psk); err != nil {
		t.Fatal(err)
	}
	tun, stack, err := netstack.CreateNetTUN([]netip.Addr{
		netip.MustParseAddr("10.77.0.1"), netip.MustParseAddr("fd00:77::1"),
	}, nil, 1420)
	if err != nil {
		t.Fatal(err)
	}
	// The portable official bind uses Go's UDP sockets. Windows' RIO bind
	// aborts its receive loop on ICMP port-unreachable during rapid reconnects.
	dev := device.NewDevice(tun, conn.NewStdNetBind(), device.NewLogger(device.LogLevelSilent, ""))
	t.Cleanup(dev.Close)
	cfg := fmt.Sprintf("private_key=%s\nlisten_port=0\npublic_key=%s\npreshared_key=%s\nallowed_ip=10.77.0.2/32\nallowed_ip=fd00:77::2/128\n",
		hex.EncodeToString(server.Bytes()), hex.EncodeToString(client.PublicKey().Bytes()), hex.EncodeToString(psk))
	if err := dev.IpcSet(cfg); err != nil {
		t.Fatal(err)
	}
	if err := dev.Up(); err != nil {
		t.Fatal(err)
	}
	status, err := dev.IpcGet()
	if err != nil {
		t.Fatal(err)
	}
	port := ""
	for _, line := range strings.Split(status, "\n") {
		if strings.HasPrefix(line, "listen_port=") {
			port = strings.TrimPrefix(line, "listen_port=")
		}
	}
	if port == "" || port == "0" {
		t.Fatal("peer did not bind a UDP port")
	}
	return &testPeer{dev: dev, stack: stack, endpoint: net.JoinHostPort("127.0.0.1", port), metadata: map[string]any{
		"privateKey":   base64.StdEncoding.EncodeToString(client.Bytes()),
		"publicKey":    base64.StdEncoding.EncodeToString(server.PublicKey().Bytes()),
		"presharedKey": base64.StdEncoding.EncodeToString(psk),
		"address":      []string{"10.77.0.2/32", "fd00:77::2/128"},
		"allowedIPs":   []string{"10.77.0.0/24", "fd00:77::/64"},
		"keepalive":    0,
		"dialTimeout":  "3s",
	}}
}

func (p *testPeer) echo(t *testing.T, network string) string {
	t.Helper()
	ip := net.ParseIP("10.77.0.1")
	if strings.HasSuffix(network, "6") {
		ip = net.ParseIP("fd00:77::1")
	}
	if strings.HasPrefix(network, "tcp") {
		ln, err := p.stack.ListenTCP(&net.TCPAddr{IP: ip})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go func() { defer c.Close(); io.Copy(c, c) }()
			}
		}()
		return ln.Addr().String()
	}
	c, err := p.stack.ListenUDP(&net.UDPAddr{IP: ip})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	go func() {
		buf := make([]byte, 65535)
		for {
			n, addr, err := c.ReadFrom(buf)
			if err != nil {
				return
			}
			if _, err := c.WriteTo(buf[:n], addr); err != nil {
				return
			}
		}
	}()
	return c.LocalAddr().String()
}

type trackedDialer struct {
	calls  atomic.Int32
	closes atomic.Int32
}
type trackedConn struct {
	net.Conn
	once  sync.Once
	owner *trackedDialer
}

func (d *trackedDialer) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "udp" {
		return nil, fmt.Errorf("unexpected plaintext underlay: %s", network)
	}
	c, err := (&net.Dialer{}).DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	d.calls.Add(1)
	return &trackedConn{Conn: c, owner: d}, nil
}
func (c *trackedConn) Close() error {
	var err error
	c.once.Do(func() { c.owner.closes.Add(1); err = c.Conn.Close() })
	return err
}

func newTestDialer(t *testing.T, metadata map[string]any) *wgDialer {
	t.Helper()
	d := NewDialer().(*wgDialer)
	if err := d.Init(md.NewMetadata(metadata)); err != nil {
		t.Fatal(err)
	}
	return d
}

func roundTrip(c net.Conn, payload []byte) error {
	if err := c.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	if _, err := c.Write(payload); err != nil {
		return err
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(c, buf); err != nil {
		return err
	}
	if !bytes.Equal(buf, payload) {
		return fmt.Errorf("payload mismatch")
	}
	return nil
}

func TestWireGuardRoundTrip(t *testing.T) {
	p := newPeer(t)
	d := newTestDialer(t, p.metadata)
	underlay := &trackedDialer{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// Keep the device alive while all TCP/UDP sessions are opened and closed.
	anchor, err := d.Dial(ctx, p.endpoint, dialer.NetDialerDialOption(underlay))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { anchor.Close() })
	for _, network := range []string{"tcp4", "udp4", "tcp6", "udp6"} {
		t.Run(network, func(t *testing.T) {
			addr := p.echo(t, network)
			lease, err := d.Dial(ctx, p.endpoint, dialer.NetDialerDialOption(underlay))
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			c, err := wgconnector.NewConnector().Connect(ctx, lease, network, addr)
			if err != nil {
				t.Fatal(err)
			}
			payload := bytes.Repeat([]byte("wireguard-encrypted-data-"), 40)
			if strings.HasPrefix(network, "tcp") {
				payload = bytes.Repeat(payload, 64)
			}
			if err := roundTrip(c, payload); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(network, "udp") {
				pc, ok := c.(net.PacketConn)
				if !ok {
					t.Fatal("UDP connection does not implement PacketConn")
				}
				if _, err := pc.WriteTo([]byte("packet"), c.RemoteAddr()); err != nil {
					t.Fatal(err)
				}
				buf := make([]byte, 20)
				n, from, err := pc.ReadFrom(buf)
				if err != nil || string(buf[:n]) != "packet" || from.String() != addr {
					t.Fatalf("PacketConn failed: %v", err)
				}
				if _, err := pc.WriteTo(buf, &net.UDPAddr{IP: net.ParseIP("10.77.0.9"), Port: 9}); err == nil {
					t.Fatal("redirected UDP packet accepted")
				}
			}
		})
	}
	if underlay.calls.Load() != 1 {
		t.Fatalf("expected one shared UDP socket, got %d", underlay.calls.Load())
	}
	status, err := p.dev.IpcGet()
	if err != nil {
		t.Fatal(err)
	}
	handshake, received := false, false
	for _, line := range strings.Split(status, "\n") {
		if strings.HasPrefix(line, "last_handshake_time_sec=") {
			n, _ := strconv.ParseInt(strings.TrimPrefix(line, "last_handshake_time_sec="), 10, 64)
			handshake = n > 0
		}
		if strings.HasPrefix(line, "rx_bytes=") {
			n, _ := strconv.ParseInt(strings.TrimPrefix(line, "rx_bytes="), 10, 64)
			received = n > 0
		}
	}
	if !handshake || !received {
		t.Fatal("peer did not record a real handshake and encrypted traffic")
	}
	if err := anchor.Close(); err != nil {
		t.Fatal(err)
	}
	if underlay.closes.Load() != 1 || len(d.tunnels) != 0 {
		t.Fatal("idle device/UDP socket leaked")
	}
}

func TestWireGuardConcurrentFlowsAndReconnect(t *testing.T) {
	p := newPeer(t)
	addr := p.echo(t, "tcp4")
	d := newTestDialer(t, p.metadata)
	u := &trackedDialer{}
	for attempt := 0; attempt < 2; attempt++ {
		anchor, err := d.Dial(context.Background(), p.endpoint, dialer.NetDialerDialOption(u))
		if err != nil {
			t.Fatal(err)
		}
		errors := make(chan error, 8)
		var workers sync.WaitGroup
		for i := 0; i < 8; i++ {
			workers.Add(1)
			go func(i int) {
				defer workers.Done()
				lease, err := d.Dial(context.Background(), p.endpoint, dialer.NetDialerDialOption(u))
				if err != nil {
					errors <- err
					return
				}
				defer lease.Close()
				c, err := wgconnector.NewConnector().Connect(context.Background(), lease, "tcp", addr)
				if err == nil {
					err = roundTrip(c, bytes.Repeat([]byte(fmt.Sprintf("flow-%d", i)), 1000))
				}
				errors <- err
			}(i)
		}
		workers.Wait()
		close(errors)
		anchor.Close()
		for err := range errors {
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if u.calls.Load() != 2 || u.closes.Load() != 2 {
		t.Fatalf("device lifecycle mismatch: open=%d close=%d", u.calls.Load(), u.closes.Load())
	}
}

func TestWireGuardRejectsWrongKeyAndHonorsCancellation(t *testing.T) {
	for _, mode := range []string{"wrong-public-key", "wrong-preshared-key", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			p := newPeer(t)
			addr := p.echo(t, "tcp4")
			p.metadata["dialTimeout"] = "250ms"
			if mode == "wrong-public-key" {
				p.metadata["publicKey"] = base64.StdEncoding.EncodeToString(newKey(t).PublicKey().Bytes())
			}
			if mode == "wrong-preshared-key" {
				p.metadata["presharedKey"] = base64.StdEncoding.EncodeToString(newKey(t).Bytes())
			}
			d := newTestDialer(t, p.metadata)
			u := &trackedDialer{}
			lease, err := d.Dial(context.Background(), p.endpoint, dialer.NetDialerDialOption(u))
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			start := time.Now()
			_, err = wgconnector.NewConnector().Connect(ctx, lease, "tcp", addr)
			if err == nil {
				t.Fatal("connection unexpectedly succeeded")
			}
			if mode == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("expected cancellation, got %v", err)
			}
			if time.Since(start) > 2*time.Second {
				t.Fatal("dial timeout not honored")
			}
			if u.closes.Load() != 1 || len(d.tunnels) != 0 {
				t.Fatal("failed connect leaked its device")
			}
		})
	}
}

func TestWireGuardRejectsTargetsOutsideAllowedIPs(t *testing.T) {
	p := newPeer(t)
	d := newTestDialer(t, p.metadata)
	for _, target := range []string{"192.0.2.1:80", "10.77.0.2:80", "[fd00:77::2]:80", "example.invalid:80"} {
		lease, err := d.Dial(context.Background(), p.endpoint)
		if err != nil {
			t.Fatal(err)
		}
		_, err = wgconnector.NewConnector().Connect(context.Background(), lease, "tcp", target)
		lease.Close()
		if err == nil {
			t.Fatalf("unexpectedly accepted %s", target)
		}
	}
}
