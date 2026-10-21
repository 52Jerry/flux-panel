package wg

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	md "github.com/go-gost/x/metadata"
)

func validMetadata(t *testing.T) map[string]any {
	return map[string]any{
		"privateKey": base64.StdEncoding.EncodeToString(newKey(t).Bytes()),
		"publicKey":  base64.StdEncoding.EncodeToString(newKey(t).PublicKey().Bytes()),
		"address":    "10.77.0.2/24, fd00:77::2/64",
		"allowedIPs": "0.0.0.0/0, ::/0",
		"dns":        []any{"10.77.0.1", "fd00:77::1"},
	}
}

func TestMetadataValidation(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		value       any
	}{
		{"missing-private", "privateKey", nil},
		{"invalid-private", "privateKey", "secret-that-must-not-appear-in-errors"},
		{"zero-private", "privateKey", base64.StdEncoding.EncodeToString(make([]byte, 32))},
		{"short-public", "publicKey", base64.StdEncoding.EncodeToString([]byte("short"))},
		{"invalid-psk", "presharedKey", "bad"},
		{"missing-address", "address", nil},
		{"invalid-address", "address", "not-an-ip"},
		{"unspecified-address", "address", "0.0.0.0"},
		{"duplicate-address", "address", "10.0.0.1,10.0.0.1/24"},
		{"non-string-address", "address", []any{"10.0.0.1", 3}},
		{"missing-allowed", "allowedIPs", nil},
		{"empty-allowed", "allowedIPs", []any{}},
		{"bad-allowed", "allowedIPs", "10.0.0.0/99"},
		{"empty-list-item", "allowedIPs", "10.0.0.0/8,"},
		{"bad-dns", "dns", "dns.example.com"},
		{"local-dns", "dns", "10.77.0.2"},
		{"small-mtu", "mtu", 100},
		{"invalid-mtu", "mtu", "invalid"},
		{"large-mtu", "mtu", 65535},
		{"negative-keepalive", "keepalive", -1},
		{"large-keepalive", "keepalive", 65536},
		{"fractional-keepalive", "keepalive", 2.5},
		{"negative-timeout", "dialTimeout", "-1s"},
		{"zero-timeout", "dialTimeout", 0},
		{"invalid-timeout", "dialTimeout", "never"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := validMetadata(t)
			m[tc.field] = tc.value
			_, err := parseMetadata(md.NewMetadata(m))
			if err == nil {
				t.Fatal("invalid metadata accepted")
			}
			privateKey, _ := m["privateKey"].(string)
			if strings.Contains(err.Error(), "secret-that-must-not-appear-in-errors") || privateKey != "" && strings.Contains(err.Error(), privateKey) {
				// Do not include keys in a diagnostic assertion either.
				t.Fatal("error disclosed key material")
			}
		})
	}
}

func TestMetadataDefaultsAndJSONNumbers(t *testing.T) {
	m := validMetadata(t)
	m["keepalive"] = float64(0)
	m["mtu"] = float64(1380)
	m["dialTimeout"] = float64(2)
	parsed, err := parseMetadata(md.NewMetadata(m))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.keepalive != 0 || parsed.mtu != 1380 || parsed.dialTimeout != 2*time.Second {
		t.Fatal("JSON numeric metadata was misinterpreted")
	}
	if got := parsed.addresses[0].String(); got != "10.77.0.2" {
		t.Fatalf("CIDR discarded host bits: %s", got)
	}
	parsed, err = parseMetadata(md.NewMetadata(validMetadata(t)))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.keepalive != 25 || parsed.mtu != 1420 || parsed.dialTimeout != 5*time.Second {
		t.Fatal("unexpected defaults")
	}
}
