package stats

import (
	"sync"
	"testing"

	"github.com/go-gost/core/observer/stats"
)

func TestAcknowledgePreservesConcurrentTraffic(t *testing.T) {
	s := NewStats(false).(*Stats)
	s.Add(stats.KindInputBytes, 100)
	s.Add(stats.KindOutputBytes, 200)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10000; j++ {
				s.Add(stats.KindInputBytes, 1)
				s.Add(stats.KindOutputBytes, 2)
			}
		}()
	}
	s.AcknowledgeTraffic(100, 200)
	wg.Wait()
	if input, output := s.Get(stats.KindInputBytes), s.Get(stats.KindOutputBytes); input != 80000 || output != 160000 {
		t.Fatalf("lost concurrent traffic: %d/%d", input, output)
	}
}
