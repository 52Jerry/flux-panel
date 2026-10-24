package wg

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"testing"

	corechain "github.com/go-gost/core/chain"
	"github.com/go-gost/x/config"
	chainparser "github.com/go-gost/x/config/parsing/chain"
	"github.com/go-gost/x/logger"
	"golang.org/x/net/dns/dnsmessage"
	"gopkg.in/yaml.v3"
)

func (p *testPeer) dns(t *testing.T) {
	t.Helper()
	c, err := p.stack.ListenUDP(&net.UDPAddr{IP: net.ParseIP("10.77.0.1"), Port: 53})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := c.ReadFrom(buf)
			if err != nil {
				return
			}
			var request dnsmessage.Message
			if request.Unpack(buf[:n]) != nil {
				continue
			}
			response := dnsmessage.Message{Header: dnsmessage.Header{ID: request.ID, Response: true, Authoritative: true, RecursionDesired: request.RecursionDesired, RecursionAvailable: true}, Questions: request.Questions}
			for _, q := range request.Questions {
				if q.Name.String() == "tunnel-only.test." && q.Type == dnsmessage.TypeA {
					response.Answers = append(response.Answers, dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 30}, Body: &dnsmessage.AResource{A: [4]byte{10, 77, 0, 1}}})
				}
			}
			if data, err := response.Pack(); err == nil {
				c.WriteTo(data, addr)
			}
		}
	}()
}

func TestWireGuardJSONAndYAMLThroughGOSTChain(t *testing.T) {
	for _, format := range []string{"json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			p := newPeer(t)
			p.dns(t)
			p.metadata["dns"] = []string{"10.77.0.1"}
			addr := p.echo(t, "tcp4")
			_, port, _ := net.SplitHostPort(addr)
			original := &config.ChainConfig{Name: "wg-chain", Hops: []*config.HopConfig{{Name: "wg-hop", Nodes: []*config.NodeConfig{{
				Name: "wg-node", Addr: p.endpoint,
				Connector: &config.ConnectorConfig{Type: "wg"},
				Dialer:    &config.DialerConfig{Type: "wg", Metadata: p.metadata},
			}}}}}
			var parsed config.ChainConfig
			if format == "json" {
				data, err := json.Marshal(original)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(data, &parsed); err != nil {
					t.Fatal(err)
				}
			} else {
				data, err := yaml.Marshal(original)
				if err != nil {
					t.Fatal(err)
				}
				if err := yaml.Unmarshal(data, &parsed); err != nil {
					t.Fatal(err)
				}
			}
			log := logger.NewLogger(logger.OutputOption(io.Discard))
			chain, err := chainparser.ParseChain(&parsed, log)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			target := net.JoinHostPort("tunnel-only.test", port)
			route := chain.Route(ctx, "tcp", target)
			c, err := route.Dial(ctx, "tcp", target, corechain.LoggerDialOption(log))
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if err := roundTrip(c, []byte("resolved-and-forwarded-through-wireguard")); err != nil {
				t.Fatal(err)
			}
		})
	}
}
