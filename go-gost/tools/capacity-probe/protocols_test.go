package main

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFixtureCertificateVerifiesOnlyItsHostname(t *testing.T) {
	root := t.TempDir()
	config, err := fixtureTLS(root)
	if err != nil {
		t.Fatal(err)
	}
	if config.InsecureSkipVerify || config.MinVersion != tls.VersionTLS13 {
		t.Fatal("fixture must exercise verified TLS 1.3")
	}
	pair, err := tls.LoadX509KeyPair(filepath.Join(root, "fixture-cert.pem"), filepath.Join(root, "fixture-key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	options := x509.VerifyOptions{Roots: config.RootCAs, DNSName: config.ServerName}
	if _, err = cert.Verify(options); err != nil {
		t.Fatal(err)
	}
	options.DNSName = "unexpected.test"
	if _, err = cert.Verify(options); err == nil {
		t.Fatal("unexpected hostname trusted")
	}
	if _, err = os.Stat(filepath.Join(root, "fixture-key.pem")); err != nil {
		t.Fatal(err)
	}
}

func TestMixedIdentitiesGetTenSessionsAcrossThreeProtocols(t *testing.T) {
	p := &protocolClients{mode: "mixed", users: 10000}
	seen := make(map[string]bool, 10000)
	counts := [3]int{}
	for user := 0; user < 10000; user++ {
		identity := fixtureUUID(user)
		if seen[identity] || fixtureUUID(user) != identity {
			t.Fatal("unstable or duplicated fixture identity")
		}
		seen[identity] = true
		protocols := make(map[string]bool)
		for connection := 0; connection < 10; connection++ {
			index := user + connection*10000
			protocols[p.protocol(index)] = true
			counts[index%3]++
		}
		if len(protocols) != 3 {
			t.Fatalf("user %d did not exercise every protocol", user)
		}
	}
	if counts != [3]int{33334, 33333, 33333} {
		t.Fatalf("unexpected mix: %v", counts)
	}
}

func TestLatencyHistogramReportsConservativeBounds(t *testing.T) {
	p := &probe{}
	for index := 0; index < 99; index++ {
		observe(&p.echoLatency, 10*time.Millisecond)
	}
	observe(&p.echoLatency, 30*time.Millisecond)
	if percentile(&p.echoLatency, 99) != 16.384 || percentile(&p.echoLatency, 100) != 32.768 {
		t.Fatal("latency bounds do not reflect the observed samples")
	}
}
