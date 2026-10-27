//go:build !linux

package tcp

import (
	"fmt"
	"net"
)

func configureCongestionControl(lc *net.ListenConfig, algorithm string) error {
	if algorithm != "" {
		return fmt.Errorf("explicit TCP congestion control requires Linux")
	}
	return nil
}
