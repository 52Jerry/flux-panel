package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	vmess "github.com/sagernet/sing-vmess"
	"github.com/sagernet/sing-vmess/vless"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
)

type protocolClients struct {
	mode  string
	users int
	vless []*vless.Client
	vmess []*vmess.Client
	tls   *tls.Config
}

func fixtureUUID(index int) string {
	return uuid.NewSHA1(uuid.Nil, []byte(fmt.Sprintf("capacity-fixture-%05d", index))).String()
}

func fixtureTLS(root string) (*tls.Config, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "capacity.test"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(24 * time.Hour), DNSNames: []string{"capacity.test"},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	encodedKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err = os.WriteFile(filepath.Join(root, "fixture-cert.pem"), cert, 0600); err != nil {
		return nil, err
	}
	if err = os.WriteFile(filepath.Join(root, "fixture-key.pem"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encodedKey}), 0600); err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(cert) {
		return nil, fmt.Errorf("fixture certificate parse failed")
	}
	return &tls.Config{RootCAs: roots, ServerName: "capacity.test", MinVersion: tls.VersionTLS13}, nil
}

func newProtocolClients(mode string, users int, tlsConfig *tls.Config) (*protocolClients, error) {
	p := &protocolClients{mode: mode, users: users, tls: tlsConfig}
	for index := 0; index < users; index++ {
		v, err := vless.NewClient(fixtureUUID(index), "xtls-rprx-vision", logger.NOP())
		if err != nil {
			return nil, err
		}
		m, err := vmess.NewClient(fixtureUUID(index), "aes-128-gcm", 0)
		if err != nil {
			return nil, err
		}
		p.vless = append(p.vless, v)
		p.vmess = append(p.vmess, m)
	}
	return p, nil
}

func (p *protocolClients) protocol(index int) string {
	if p.mode == "mixed" {
		return []string{"socks", "vless", "vmess"}[index%3]
	}
	return p.mode
}

func (p *protocolClients) dial(ctx context.Context, index int) (net.Conn, error) {
	protocol := p.protocol(index)
	if protocol == "socks" {
		return socksConnection(ctx, index, p.users)
	}
	port := 25110
	if protocol == "vmess" {
		port = 25120
	}
	source := net.ParseIP(fmt.Sprintf("127.0.0.%d", 2+index%8))
	dialer := net.Dialer{LocalAddr: &net.TCPAddr{IP: source}, Timeout: 20 * time.Second}
	raw, err := dialer.DialContext(ctx, "tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			raw.Close()
		}
	}()
	raw.SetDeadline(time.Now().Add(30 * time.Second))
	destination := M.ParseSocksaddr(fmt.Sprintf("127.0.0.1:%d", 25200+index%8))
	var result net.Conn
	if protocol == "vless" {
		secure := tls.Client(raw, p.tls)
		if err = secure.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		result, err = p.vless[index%p.users].DialConn(secure, destination)
	} else {
		result, err = p.vmess[index%p.users].DialConn(raw, destination)
	}
	if err != nil {
		return nil, err
	}
	ok = true
	return result, nil
}
