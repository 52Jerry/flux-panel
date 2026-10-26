package wrapper

import (
	"bytes"
	"io"
	"net"

	"github.com/go-gost/core/limiter"
	"github.com/go-gost/core/limiter/traffic"
	xnet "github.com/go-gost/x/internal/net"
)

// readWriter is an io.ReadWriter with traffic limiter supported.
type readWriter struct {
	io.ReadWriter
	rbuf    bytes.Buffer
	readErr error
	limiter traffic.TrafficLimiter
	opts    []limiter.Option
	key     string
	life    connLifetime
}

func WrapReadWriter(limiter traffic.TrafficLimiter, rw io.ReadWriter, key string, opts ...limiter.Option) io.ReadWriter {
	if limiter == nil {
		return rw
	}

	return &readWriter{
		ReadWriter: rw,
		limiter:    limiter,
		opts:       opts,
		key:        key,
		life:       newConnLifetime(),
	}
}

func (p *readWriter) Read(b []byte) (n int, err error) {
	if len(b) == 0 {
		return 0, nil
	}
	limiter := p.limiter.In(p.life.ctx, p.key, p.opts...)
	if limiter == nil || limiter.Limit() <= 0 {
		if p.rbuf.Len() > 0 {
			n, err = p.rbuf.Read(b)
			if p.rbuf.Len() == 0 && p.readErr != nil {
				err = p.readErr
				p.readErr = nil
			}
			return
		}
		return p.ReadWriter.Read(b)
	}

	if p.rbuf.Len() > 0 {
		burst := len(b)
		if p.rbuf.Len() < burst {
			burst = p.rbuf.Len()
		}
		lim := p.life.wait(limiter, burst)
		if p.life.ctx.Err() != nil {
			return 0, net.ErrClosed
		}
		if lim <= 0 {
			return 0, io.ErrNoProgress
		}
		n, err = p.rbuf.Read(b[:lim])
		if p.rbuf.Len() == 0 && p.readErr != nil {
			err = p.readErr
			p.readErr = nil
		}
		return
	}

	nn, readErr := p.ReadWriter.Read(b)
	if nn == 0 {
		return 0, readErr
	}

	n = p.life.wait(limiter, nn)
	if p.life.ctx.Err() != nil {
		return 0, net.ErrClosed
	}
	if n <= 0 && nn > 0 {
		return 0, io.ErrNoProgress
	}
	if n < nn {
		if _, err = p.rbuf.Write(b[n:nn]); err != nil {
			return 0, err
		}
		p.readErr = readErr
	} else {
		err = readErr
	}

	return
}

func (p *readWriter) Write(b []byte) (n int, err error) {
	return p.WriteWithOptions(b, xnet.WriteOptions{})
}

func (p *readWriter) WriteWithOptions(b []byte, options xnet.WriteOptions) (n int, err error) {
	limiter := p.limiter.Out(p.life.ctx, p.key, p.opts...)
	if limiter == nil || limiter.Limit() <= 0 {
		return xnet.WriteWithOptions(p.ReadWriter, b, options)
	}

	nn := 0
	for len(b) > 0 {
		granted := p.life.wait(limiter, len(b))
		if p.life.ctx.Err() != nil {
			return n, net.ErrClosed
		}
		if granted <= 0 {
			return n, io.ErrNoProgress
		}
		nn, err = xnet.WriteWithOptions(p.ReadWriter, b[:granted], options)
		n += nn
		if err != nil {
			return
		}
		if nn <= 0 {
			return n, io.ErrNoProgress
		}
		b = b[nn:]
	}

	return
}

func (p *readWriter) Close() error {
	if closer, ok := p.ReadWriter.(io.Closer); ok {
		return p.life.close(closer)
	}
	p.life.once.Do(p.life.cancel)
	return nil
}

// CancelPending lets an outer connection owner cancel shaping without closing
// or double-counting the same underlying socket through multiple wrappers.
func (p *readWriter) CancelPending() { p.life.cancel() }
