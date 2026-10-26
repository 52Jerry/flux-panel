package socket

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	core "github.com/go-gost/core/limiter"
	"github.com/go-gost/x/config"
	xnet "github.com/go-gost/x/internal/net"
	limitercache "github.com/go-gost/x/limiter/traffic/cache"
	"github.com/go-gost/x/limiter/traffic/wrapper"
	"github.com/go-gost/x/registry"
)

func TestUpdateLimiterKeepsActivePacedConnection(t *testing.T) {
	for _, test := range []struct {
		name, before, after string
		payload             string
	}{
		{"increase", "$ 1B 1B", "$ 10000B 10000B", "continuous"},
		{"decrease", "$ 100B 100B", "$ 20B 20B", "continuous data"},
	} {
		t.Run(test.name, func(t *testing.T) {
			oldConfig := config.Global()
			config.Set(&config.Config{})
			t.Cleanup(func() { config.Set(oldConfig) })
			name := "test-live-update-" + test.name
			cfg := config.LimiterConfig{Name: name, Limits: []string{test.before}}
			if err := createLimiter(createLimiterRequest{Data: cfg}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { registry.TrafficLimiterRegistry().Unregister(name) })
			provider := registry.TrafficLimiterRegistry().Get(name)
			opts := []core.Option{core.ScopeOption(core.ScopeService), core.ServiceOption("1_2_3_tcp")}
			before := provider.Out(context.Background(), "$", opts...)
			identity := before.(interface{ CacheIdentity() any }).CacheIdentity()
			left, right := net.Pipe()
			defer right.Close()
			conn := wrapper.WrapConn(left, limitercache.NewCachedTrafficLimiter(provider), "$", opts...)
			defer conn.Close()
			firstByte := make(chan struct{}, 1)
			type result struct {
				n   int
				err error
			}
			written := make(chan result, 1)
			read := make(chan result, 1)
			buf := make([]byte, len(test.payload))
			go func() { n, err := io.ReadFull(right, buf); read <- result{n, err} }()
			go func() {
				n, err := xnet.WriteWithOptions(conn, []byte(test.payload), xnet.WriteOptions{
					Timeout: 30 * time.Millisecond,
					OnProgress: func(int) {
						select {
						case firstByte <- struct{}{}:
						default:
						}
					},
				})
				written <- result{n, err}
			}()
			select {
			case <-firstByte:
			case <-time.After(time.Second):
				t.Fatal("initial burst was not delivered")
			}
			// The initial burst has been consumed. The same Write is now waiting
			// for tokens, which Unregister/Close used to interrupt.
			time.Sleep(20 * time.Millisecond)
			cfg.Limits = []string{test.after}
			if err := updateLimiter(updateLimiterRequest{Limiter: name, Data: cfg}); err != nil {
				t.Fatal(err)
			}
			after := provider.Out(context.Background(), "$", opts...)
			if after.(interface{ CacheIdentity() any }).CacheIdentity() != identity {
				t.Fatal("speed update replaced the live bucket")
			}
			if before.Limit() != after.Limit() || before.(interface{ Closed() bool }).Closed() {
				t.Fatal("existing connection did not retain the updated bucket")
			}
			select {
			case result := <-written:
				if result.err != nil || result.n != len(test.payload) {
					t.Fatalf("speed update interrupted active Write: %d %v", result.n, result.err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("updated transfer did not finish")
			}
			select {
			case result := <-read:
				if result.err != nil || string(buf) != test.payload {
					t.Fatalf("payload changed: %q %v", buf, result.err)
				}
			case <-time.After(time.Second):
				t.Fatal("peer did not receive full payload")
			}
		})
	}
}

func TestUpdateLimiterPolicyChangeRetiresOldWaiters(t *testing.T) {
	oldConfig := config.Global()
	config.Set(&config.Config{})
	t.Cleanup(func() { config.Set(oldConfig) })
	name := "test-policy-update"
	cfg := config.LimiterConfig{Name: name, Limits: []string{"$ 1B 1B"}}
	if err := createLimiter(createLimiterRequest{Data: cfg}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { registry.TrafficLimiterRegistry().Unregister(name) })
	provider := registry.TrafficLimiterRegistry().Get(name)
	left, right := net.Pipe()
	defer right.Close()
	conn := wrapper.WrapConn(left, provider, "$", core.ScopeOption(core.ScopeService))
	defer conn.Close()
	first := make(chan struct{})
	go func() { one := make([]byte, 1); right.Read(one); close(first); io.Copy(io.Discard, right) }()
	done := make(chan error, 1)
	go func() { _, err := conn.Write([]byte("ab")); done <- err }()
	select {
	case <-first:
	case <-time.After(time.Second):
		t.Fatal("no initial progress")
	}
	cfg.Metadata = map[string]any{"traffic.fairness": "none"}
	if err := updateLimiter(updateLimiterRequest{Limiter: name, Data: cfg}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, io.ErrNoProgress) {
			t.Fatalf("old policy waiter returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("replaced policy retained its old waiter")
	}
	if provider.Out(context.Background(), "$", core.ScopeOption(core.ScopeService)) == nil {
		t.Fatal("replacement lost the registered limiter")
	}
}
