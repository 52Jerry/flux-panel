package dialer

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestSourcePoolKeepsLegacyOrderAndRejectsInvalidConfiguration(t *testing.T) {
	got, err := sourceInterfaces("eth0!,eth1", "tcp", "example.test:1")
	if err != nil || strings.Join(got, ",") != "eth0!,eth1" {
		t.Fatalf("%v %v", got, err)
	}
	for _, input := range []string{"pool:", "pool:192.0.2.1", "pool:192.0.2.1,example.test", "pool:192.0.2.1,192.0.2.1", "pool:192.0.2.1,"} {
		if _, err := sourceInterfaces(input, "tcp", "example.test:1"); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}

func TestSourcePoolBalancesTenThousandConcurrentSelections(t *testing.T) {
	var addresses []string
	for i := 1; i <= 8; i++ {
		addresses = append(addresses, fmt.Sprintf("192.0.2.%d", i))
	}
	pool := "pool:" + strings.Join(addresses, ",")
	counts := make(map[string]int)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				items, err := sourceInterfaces(pool, "tcp", "198.51.100.1:20168")
				if err != nil {
					t.Error(err)
					return
				}
				if len(items) != 8 {
					t.Errorf("lost fallback source: %v", items)
					return
				}
				for _, item := range items {
					if !strings.HasSuffix(item, "!") {
						t.Error("unbound fallback allowed")
						return
					}
				}
				mu.Lock()
				counts[items[0]]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	for _, address := range addresses {
		if counts[address+"!"] != 1250 {
			t.Fatalf("unbalanced source counts: %v", counts)
		}
	}
}
