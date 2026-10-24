package wg

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	md "github.com/go-gost/core/metadata"
)

type metadata struct {
	privateKey, publicKey, presharedKey string
	addresses, dns                      []netip.Addr
	allowedIPs                          []netip.Prefix
	mtu, keepalive                      int
	dialTimeout                         time.Duration
}

func parseMetadata(md md.Metadata) (m metadata, err error) {
	value := func(key string) any {
		if md == nil {
			return nil
		}
		return md.Get(key)
	}
	key := func(name string, optional bool) (string, error) {
		v := value(name)
		if v == nil && optional {
			return "", nil
		}
		s, ok := v.(string)
		b, e := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
		if !ok || e != nil || len(b) != 32 {
			return "", fmt.Errorf("wg: %s must be a base64-encoded 32-byte key", name)
		}
		var nonzero byte
		for _, v := range b {
			nonzero |= v
		}
		if nonzero == 0 && !optional {
			return "", fmt.Errorf("wg: %s must not be zero", name)
		}
		return hex.EncodeToString(b), nil
	}
	if m.privateKey, err = key("privateKey", false); err != nil {
		return
	}
	if m.publicKey, err = key("publicKey", false); err != nil {
		return
	}
	if m.presharedKey, err = key("presharedKey", true); err != nil {
		return
	}
	list := func(name string, required bool) ([]string, error) {
		var result []string
		switch v := value(name).(type) {
		case nil:
		case string:
			result = strings.Split(v, ",")
		case []string:
			result = append(result, v...)
		case []any:
			for _, item := range v {
				s, ok := item.(string)
				if !ok {
					return nil, fmt.Errorf("wg: %s must contain strings", name)
				}
				result = append(result, s)
			}
		default:
			return nil, fmt.Errorf("wg: %s must be a string or list of strings", name)
		}
		if required && len(result) == 0 {
			return nil, fmt.Errorf("wg: %s is required", name)
		}
		for i := range result {
			result[i] = strings.TrimSpace(result[i])
			if result[i] == "" {
				return nil, fmt.Errorf("wg: %s contains an empty entry", name)
			}
		}
		return result, nil
	}
	addresses, err := list("address", true)
	if err != nil {
		return m, err
	}
	seen := make(map[netip.Addr]bool)
	for _, s := range addresses {
		ip, e := netip.ParseAddr(s)
		if e != nil {
			prefix, pe := netip.ParsePrefix(s)
			if pe != nil {
				return m, fmt.Errorf("wg: invalid address %q", s)
			}
			ip = prefix.Addr()
		}
		ip = ip.Unmap()
		if ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() || seen[ip] {
			return m, fmt.Errorf("wg: invalid or duplicate address %q", s)
		}
		seen[ip] = true
		m.addresses = append(m.addresses, ip)
	}
	allowed, err := list("allowedIPs", true)
	if err != nil {
		return m, err
	}
	for _, s := range allowed {
		prefix, e := netip.ParsePrefix(s)
		if e != nil || prefix.Addr().Is4In6() {
			return m, fmt.Errorf("wg: invalid allowedIPs prefix %q", s)
		}
		m.allowedIPs = append(m.allowedIPs, prefix.Masked())
	}
	dns, err := list("dns", false)
	if err != nil {
		return m, err
	}
	for _, s := range dns {
		ip, e := netip.ParseAddr(s)
		if e != nil || ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() {
			return m, fmt.Errorf("wg: invalid DNS server %q", s)
		}
		ip = ip.Unmap()
		if !m.allows(ip) {
			return m, fmt.Errorf("wg: DNS server %s is outside allowedIPs or has no local address family", ip)
		}
		m.dns = append(m.dns, ip)
	}
	integer := func(name string, def, min, max int) (int, error) {
		v := value(name)
		if v == nil {
			return def, nil
		}
		n, e := strconv.Atoi(fmt.Sprint(v))
		if e != nil || n < min || n > max {
			return 0, fmt.Errorf("wg: %s must be an integer between %d and %d", name, min, max)
		}
		return n, nil
	}
	if m.mtu, err = integer("mtu", 1420, 1280, 65535-80); err != nil {
		return
	}
	if m.keepalive, err = integer("keepalive", 25, 0, 65535); err != nil {
		return
	}
	m.dialTimeout = 5 * time.Second
	if v := value("dialTimeout"); v != nil {
		s := fmt.Sprint(v)
		m.dialTimeout, err = time.ParseDuration(s)
		if err != nil {
			m.dialTimeout, err = time.ParseDuration(s + "s")
		}
		if err != nil || m.dialTimeout <= 0 {
			return m, fmt.Errorf("wg: dialTimeout must be a positive duration")
		}
	}
	return m, nil
}

func (m metadata) allows(ip netip.Addr) bool {
	for _, local := range m.addresses {
		if local == ip {
			// netstack handles local traffic internally, without WireGuard.
			return false
		}
	}
	for _, local := range m.addresses {
		if local.Is4() != ip.Is4() {
			continue
		}
		for _, prefix := range m.allowedIPs {
			if prefix.Contains(ip) {
				return true
			}
		}
	}
	return false
}
