package relay

import (
	xmetadata "github.com/go-gost/x/metadata"
	"testing"
	"time"
)

func TestTransportTimeoutMetadata(t *testing.T) {
	h := &relayHandler{}
	if err := h.parseMetadata(nil); err != nil || h.md.writeTimeout != 60*time.Second || h.md.idleTimeout != 300*time.Second {
		t.Fatalf("defaults %+v/%v", h.md, err)
	}
	md := xmetadata.NewMetadata(map[string]any{"writeTimeout": float64(0), "idleTimeout": 0})
	if err := h.parseMetadata(md); err != nil || h.md.writeTimeout != 0 || h.md.idleTimeout != 0 {
		t.Fatalf("disable %+v/%v", h.md, err)
	}
	entry := &tcpHandler{}
	if err := entry.Init(nil); err != nil || entry.transportOptions.WriteTimeout != 60*time.Second || entry.transportOptions.IdleTimeout != 0 {
		t.Fatalf("entry default %+v/%v", entry.transportOptions, err)
	}
	if err := entry.Init(md); err != nil || entry.transportOptions.WriteTimeout != 0 {
		t.Fatalf("entry disable %+v/%v", entry.transportOptions, err)
	}
}
