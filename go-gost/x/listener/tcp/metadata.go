package tcp

import (
	"fmt"
	"strings"

	md "github.com/go-gost/core/metadata"
	mdutil "github.com/go-gost/x/metadata/util"
)

type metadata struct {
	mptcp             bool
	congestionControl string
}

func (l *tcpListener) parseMetadata(md md.Metadata) (err error) {
	l.md.mptcp = mdutil.GetBool(md, "mptcp")
	l.md.congestionControl = ""
	if md != nil && md.IsExists("tcp.congestionControl") {
		value, ok := md.Get("tcp.congestionControl").(string)
		if !ok {
			return fmt.Errorf("tcp.congestionControl must be a string")
		}
		value = strings.TrimSpace(value)
		switch value {
		case "", "reno", "cubic", "bbr":
			l.md.congestionControl = value
		default:
			return fmt.Errorf("unsupported tcp.congestionControl %q", value)
		}
	}

	return
}
