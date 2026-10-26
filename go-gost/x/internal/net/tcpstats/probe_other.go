//go:build !linux

package tcpstats

import "net"

const platformSupported = false

func probeConn(net.Conn) (observation, error) { return observation{}, errUnavailable }
