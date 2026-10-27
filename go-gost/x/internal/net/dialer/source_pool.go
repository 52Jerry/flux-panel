package dialer

import (
	"fmt"
	"hash/fnv"
	"net"
	"strings"
	"sync/atomic"
)

// Fixed stripes bound bookkeeping even when routes/configurations are replaced.
var sourcePoolSequence [64]atomic.Uint64

// pool: selects a round-robin starting source, retaining strict, ordered
// fallback inside the configured pool. Plain interface lists keep their
// historical ordered failover behavior.
func sourceInterfaces(value, network, destination string) ([]string, error) {
	if !strings.HasPrefix(value, "pool:") {
		return strings.Split(value, ","), nil
	}
	items, err := parseSourcePool(value)
	if err != nil {
		return nil, err
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(value))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(network))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(destination))
	start := int((sourcePoolSequence[hash.Sum32()%uint32(len(sourcePoolSequence))].Add(1) - 1) % uint64(len(items)))
	result := make([]string, 0, len(items))
	result = append(result, items[start:]...)
	result = append(result, items[:start]...)
	return result, nil
}

// ValidateSourcePool lets configuration loaders reject malformed pools before
// opening listeners. Legacy interface names and ordered lists are unchanged.
func ValidateSourcePool(value string) error {
	if !strings.HasPrefix(value, "pool:") {
		return nil
	}
	_, err := parseSourcePool(value)
	return err
}

func parseSourcePool(value string) ([]string, error) {
	items := strings.Split(strings.TrimPrefix(value, "pool:"), ",")
	if len(items) < 2 || len(items) > 16 {
		return nil, fmt.Errorf("source IP pool requires 2..16 addresses")
	}
	seen := make(map[string]struct{}, len(items))
	for index, item := range items {
		ip := net.ParseIP(strings.TrimSpace(item))
		if ip == nil {
			return nil, fmt.Errorf("source IP pool contains an invalid IP address")
		}
		key := ip.String()
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("source IP pool contains a duplicate address")
		}
		seen[key] = struct{}{}
		// Strict sources never fall through to an unbound/public default route.
		items[index] = key + "!"
	}
	return items, nil
}
