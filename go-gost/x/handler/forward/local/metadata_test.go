package local

import (
	"encoding/json"
	xmetadata "github.com/go-gost/x/metadata"
	"testing"
	"time"
)

func TestTCPBufferSizeFromJSONMetadata(t *testing.T) {
	var values map[string]any
	if err := json.Unmarshal([]byte("{\"tcp.bufferSize\":4096,\"tcp.socketBufferSize\":65536}"), &values); err != nil {
		t.Fatal(err)
	}
	h := &forwardHandler{}
	if err := h.parseMetadata(xmetadata.NewMetadata(values)); err != nil || h.md.tcpBufferSize != 4096 || h.md.tcpSocketBufferSize != 65536 {
		t.Fatalf("JSON buffer size did not apply: %d/%v", h.md.tcpBufferSize, err)
	}
	for _, value := range []any{true, "bad", -1, 4096.5, 1024, 65537} {
		if err := h.parseMetadata(xmetadata.NewMetadata(map[string]any{"tcp.bufferSize": value})); err == nil {
			t.Fatalf("accepted invalid buffer size: %v", value)
		}
	}
}

func TestWriteTimeoutMetadata(t *testing.T) {
	h := &forwardHandler{}
	if err := h.parseMetadata(nil); err != nil || h.md.writeTimeout != 60*time.Second {
		t.Fatalf("default %s/%v", h.md.writeTimeout, err)
	}
	if err := h.parseMetadata(xmetadata.NewMetadata(map[string]any{"writeTimeout": float64(0)})); err != nil || h.md.writeTimeout != 0 {
		t.Fatalf("disabled %s/%v", h.md.writeTimeout, err)
	}
	if err := h.parseMetadata(xmetadata.NewMetadata(map[string]any{"writeTimeout": -1})); err == nil {
		t.Fatal("negative timeout accepted")
	}
}
