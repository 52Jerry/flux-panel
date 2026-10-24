package wg

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/go-gost/core/dialer"
	md "github.com/go-gost/core/metadata"
	"github.com/go-gost/x/registry"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

func init() {
	registry.DialerRegistry().Register("wg", NewDialer)
	registry.DialerRegistry().Register("wireguard", NewDialer)
}

type wgDialer struct {
	md          metadata
	lock        chan struct{}
	tunnels     map[string]*tunnel
	initialized bool
	reopenAfter time.Time
}

func NewDialer(opts ...dialer.Option) dialer.Dialer {
	options := &dialer.Options{}
	for _, opt := range opts {
		opt(options)
	}

	return &wgDialer{
		lock:    make(chan struct{}, 1),
		tunnels: make(map[string]*tunnel),
	}
}

func (d *wgDialer) Init(md md.Metadata) error {
	m, err := parseMetadata(md)
	if err != nil {
		return err
	}
	d.lock <- struct{}{}
	defer func() { <-d.lock }()
	if len(d.tunnels) != 0 {
		return fmt.Errorf("wg: cannot reinitialize an active dialer")
	}
	d.md, d.initialized = m, true
	return nil
}

// Multiplex makes GOST pass the preceding route as the UDP underlay. All
// connections to a peer share one device, so concurrent flows cannot cause
// the remote peer to roam between competing WireGuard sessions.
func (d *wgDialer) Multiplex() bool { return true }

func (d *wgDialer) Dial(ctx context.Context, addr string, opts ...dialer.DialOption) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	n, portErr := strconv.Atoi(port)
	if err != nil || host == "" || strings.ContainsAny(host, "\r\n\t ") || portErr != nil || n < 1 || n > 65535 {
		return nil, fmt.Errorf("wg: endpoint must be host:port with a port between 1 and 65535")
	}
	var options dialer.DialOptions
	for _, opt := range opts {
		opt(&options)
	}

	// Waiting for another connection's device creation is also cancellable.
	select {
	case d.lock <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-d.lock }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !d.initialized {
		return nil, fmt.Errorf("wg: dialer is not initialized")
	}
	t := d.tunnels[addr]
	if t == nil {
		ctx, cancel := context.WithTimeout(ctx, d.md.dialTimeout)
		defer cancel()
		// A freshly created device needs a new handshake. WireGuard drops
		// initiations arriving within 20ms of the previous one as a flood.
		// Leave a small, cancellable gap after releasing the previous device.
		if delay := time.Until(d.reopenAfter); delay > 0 {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		bind := &udpBind{
			endpoint: &endpoint{address: addr},
			dial: func() (net.Conn, error) {
				if options.Dialer != nil {
					return options.Dialer.Dial(ctx, "udp", addr)
				}
				return (&net.Dialer{}).DialContext(ctx, "udp", addr)
			},
		}
		tun, stack, err := netstack.CreateNetTUN(d.md.addresses, d.md.dns, d.md.mtu)
		if err != nil {
			return nil, fmt.Errorf("wg: create netstack: %w", err)
		}
		dev := device.NewDevice(tun, bind, device.NewLogger(device.LogLevelSilent, ""))
		var cfg strings.Builder
		fmt.Fprintf(&cfg, "private_key=%s\npublic_key=%s\nendpoint=%s\npersistent_keepalive_interval=%d\n", d.md.privateKey, d.md.publicKey, addr, d.md.keepalive)
		if d.md.presharedKey != "" {
			fmt.Fprintf(&cfg, "preshared_key=%s\n", d.md.presharedKey)
		}
		for _, prefix := range d.md.allowedIPs {
			fmt.Fprintf(&cfg, "allowed_ip=%s\n", prefix)
		}
		if err := dev.IpcSet(cfg.String()); err != nil {
			dev.Close()
			return nil, fmt.Errorf("wg: configure device: %w", err)
		}
		if err := dev.Up(); err != nil {
			dev.Close()
			return nil, fmt.Errorf("wg: open UDP transport: %w", err)
		}
		t = &tunnel{device: dev, stack: stack, md: d.md}
		d.tunnels[addr] = t
	}
	t.refs++
	return &sessionConn{tunnel: t, peer: &endpoint{address: addr}, release: func() {
		d.lock <- struct{}{}
		defer func() { <-d.lock }()
		t.refs--
		if t.refs == 0 {
			delete(d.tunnels, addr)
			t.device.Close()
			d.reopenAfter = time.Now().Add(2 * device.HandshakeInitationRate)
		}
	}}, nil
}
