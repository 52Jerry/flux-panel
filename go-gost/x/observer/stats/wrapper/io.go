package wrapper

import (
	"io"

	"github.com/go-gost/core/observer/stats"
	xnet "github.com/go-gost/x/internal/net"
)

// readWriter is an io.ReadWriter with Stats.
type readWriter struct {
	io.ReadWriter
	stats stats.Stats
}

func WrapReadWriter(rw io.ReadWriter, stats stats.Stats) io.ReadWriter {
	if rw == nil || stats == nil {
		return rw
	}

	return &readWriter{
		ReadWriter: rw,
		stats:      stats,
	}
}

func (p *readWriter) Read(b []byte) (n int, err error) {
	n, err = p.ReadWriter.Read(b)
	p.stats.Add(stats.KindInputBytes, int64(n))

	return
}

func (p *readWriter) Write(b []byte) (n int, err error) {
	return p.WriteWithOptions(b, xnet.WriteOptions{})
}

func (p *readWriter) WriteWithOptions(b []byte, opts xnet.WriteOptions) (n int, err error) {
	n, err = xnet.WriteWithOptions(p.ReadWriter, b, opts)
	p.stats.Add(stats.KindOutputBytes, int64(n))

	return
}

func (p *readWriter) CancelPending() {
	if pending, ok := p.ReadWriter.(interface{ CancelPending() }); ok {
		pending.CancelPending()
	}
}
