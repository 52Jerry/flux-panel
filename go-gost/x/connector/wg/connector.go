package wg

import (
	"context"
	"fmt"
	"net"

	"github.com/go-gost/core/connector"
	"github.com/go-gost/core/metadata"
	"github.com/go-gost/x/registry"
)

func init() {
	registry.ConnectorRegistry().Register("wg", NewConnector)
	registry.ConnectorRegistry().Register("wireguard", NewConnector)
}

type wgConnector struct{}

func NewConnector(opts ...connector.Option) connector.Connector { return &wgConnector{} }
func (*wgConnector) Init(metadata.Metadata) error               { return nil }

func (*wgConnector) Connect(ctx context.Context, c net.Conn, network, address string, opts ...connector.ConnectOption) (net.Conn, error) {
	tunnel, ok := c.(interface {
		Connect(context.Context, string, string) (net.Conn, error)
	})
	if !ok {
		return nil, fmt.Errorf("wg: connector requires the wg dialer")
	}
	return tunnel.Connect(ctx, network, address)
}
