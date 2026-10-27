package tcp

import (
	"testing"

	xmd "github.com/go-gost/x/metadata"
)

func TestCongestionMetadata(t *testing.T) {
	for _, value := range []any{"", "reno", "cubic", " bbr "} {
		l := &tcpListener{}
		if err := l.parseMetadata(xmd.NewMetadata(map[string]any{"tcp.congestionControl": value})); err != nil {
			t.Fatalf("%v: %v", value, err)
		}
		if err := l.parseMetadata(nil); err != nil || l.md.congestionControl != "" {
			t.Fatal("omitted metadata must reset to the platform default")
		}
	}
	for _, value := range []any{true, 123, nil, "unknown", "bbr\x00", "BBR"} {
		l := &tcpListener{}
		if err := l.parseMetadata(xmd.NewMetadata(map[string]any{"tcp.congestionControl": value})); err == nil {
			t.Fatalf("invalid algorithm %v accepted", value)
		}
	}
}
