package remote

import (
	xmetadata "github.com/go-gost/x/metadata"
	"testing"
	"time"
)

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
