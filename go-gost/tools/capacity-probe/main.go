// capacity-probe runs synthetic identities against isolated forwarder and
// sing-box processes. Run it in a new network namespace, never on public ports.
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/bits"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type session struct {
	conn   net.Conn
	mu     sync.Mutex
	queued atomic.Bool
	alive  atomic.Bool
}
type probe struct {
	ctx            context.Context
	slots          []atomic.Pointer[session]
	perUser        []atomic.Int32
	active         atomic.Int64
	attempted      atomic.Int64
	connected      atomic.Int64
	failures       atomic.Int64
	echoes         atomic.Int64
	verifiedBytes  atomic.Int64
	lastError      atomic.Value
	payload        []byte
	echoLatency    [32]atomic.Uint64
	connectLatency [32]atomic.Uint64
	clients        *protocolClients
	quiescing      atomic.Bool
}

func observe(hist *[32]atomic.Uint64, duration time.Duration) {
	bucket := bits.Len64(uint64(max(duration.Microseconds(), 1)))
	if bucket >= len(hist) {
		bucket = len(hist) - 1
	}
	hist[bucket].Add(1)
}

func percentile(hist *[32]atomic.Uint64, percent uint64) float64 {
	var total uint64
	for index := range hist {
		total += hist[index].Load()
	}
	target := (total*percent + 99) / 100
	var current uint64
	for index := range hist {
		current += hist[index].Load()
		if current >= target && total > 0 {
			return float64(uint64(1)<<index) / 1000
		}
	}
	return 0
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(path+".tmp", data, 0600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

func processMemory(pid int) map[string]int64 {
	out := map[string]int64{}
	data, _ := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[0] == "VmRSS:" || fields[0] == "VmHWM:" || fields[0] == "VmSize:" || fields[0] == "Threads:" {
			out[strings.TrimSuffix(fields[0], ":")], _ = strconv.ParseInt(fields[1], 10, 64)
		}
	}
	return out
}

func availableMemory() int64 {
	data, _ := os.ReadFile("/proc/meminfo")
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 1 && fields[0] == "MemAvailable:" {
			value, _ := strconv.ParseInt(fields[1], 10, 64)
			return value * 1024
		}
	}
	return 0
}

func echoListeners(ctx context.Context) ([]net.Listener, error) {
	var listeners []net.Listener
	for port := 25200; port < 25208; port++ {
		ln, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			for _, l := range listeners {
				l.Close()
			}
			return nil, err
		}
		listeners = append(listeners, ln)
		go func(ln net.Listener) {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				go func(c net.Conn) {
					defer c.Close()
					buf := make([]byte, 4096)
					for {
						n, err := c.Read(buf)
						if n > 0 {
							if _, e := c.Write(buf[:n]); e != nil {
								return
							}
						}
						if err != nil {
							return
						}
						select {
						case <-ctx.Done():
							return
						default:
						}
					}
				}(conn)
			}
		}(ln)
	}
	return listeners, nil
}

func (p *probe) failure(err error) {
	if p.ctx.Err() != nil {
		return
	}
	p.failures.Add(1)
	// Fixture data only; do not print client identity/passwords.
	p.lastError.Store(fmt.Sprintf("%T: %v", err, err))
}

func socksConnection(ctx context.Context, index, users int) (net.Conn, error) {
	source := net.ParseIP(fmt.Sprintf("127.0.0.%d", 2+index%8))
	dialer := net.Dialer{LocalAddr: &net.TCPAddr{IP: source}, Timeout: 20 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp4", "127.0.0.1:25100")
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			conn.Close()
		}
	}()
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	if _, err = conn.Write([]byte{5, 1, 2}); err != nil {
		return nil, err
	}
	var response [2]byte
	if _, err = io.ReadFull(conn, response[:]); err != nil {
		return nil, err
	}
	if response != [2]byte{5, 2} {
		return nil, fmt.Errorf("SOCKS auth method rejected")
	}
	user := fmt.Sprintf("capacity-%05d", index%users)
	password := "isolated-capacity-fixture-only"
	auth := append([]byte{1, byte(len(user))}, []byte(user)...)
	auth = append(auth, byte(len(password)))
	auth = append(auth, []byte(password)...)
	if _, err = conn.Write(auth); err != nil {
		return nil, err
	}
	if _, err = io.ReadFull(conn, response[:]); err != nil {
		return nil, err
	}
	if response != [2]byte{1, 0} {
		return nil, fmt.Errorf("SOCKS identity rejected")
	}
	request := []byte{5, 1, 0, 1, 127, 0, 0, 1, 0, 0}
	binary.BigEndian.PutUint16(request[8:], uint16(25200+index%8))
	if _, err = conn.Write(request); err != nil {
		return nil, err
	}
	var head [4]byte
	if _, err = io.ReadFull(conn, head[:]); err != nil {
		return nil, err
	}
	if head[0] != 5 || head[1] != 0 {
		return nil, fmt.Errorf("SOCKS connect rejected: %d", head[1])
	}
	length := 0
	switch head[3] {
	case 1:
		length = 4
	case 4:
		length = 16
	case 3:
		var b [1]byte
		if _, err = io.ReadFull(conn, b[:]); err != nil {
			return nil, err
		}
		length = int(b[0])
	default:
		return nil, fmt.Errorf("invalid SOCKS reply")
	}
	if _, err = io.CopyN(io.Discard, conn, int64(length+2)); err != nil {
		return nil, err
	}
	conn.SetDeadline(time.Time{})
	ok = true
	return conn, nil
}

func verifyEcho(s *session, payload, received []byte) error {
	s.conn.SetDeadline(time.Now().Add(30 * time.Second))
	n, err := s.conn.Write(payload)
	if err != nil {
		return err
	}
	if n != len(payload) {
		return io.ErrShortWrite
	}
	if _, err = io.ReadFull(s.conn, received); err != nil {
		return err
	}
	if !bytes.Equal(payload, received) {
		return fmt.Errorf("echo payload mismatch")
	}
	s.conn.SetDeadline(time.Time{})
	return nil
}

func (p *probe) ramp(users, rate int) <-chan struct{} {
	done := make(chan struct{})
	jobs := make(chan int)
	go func() {
		defer close(jobs)
		ticker := time.NewTicker(time.Second / time.Duration(rate))
		defer ticker.Stop()
		for index := range p.slots {
			select {
			case <-p.ctx.Done():
				return
			case <-ticker.C:
			}
			select {
			case <-p.ctx.Done():
				return
			case jobs <- index:
			}
		}
	}()
	var wg sync.WaitGroup
	for worker := 0; worker < 64; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			received := make([]byte, len(p.payload))
			for index := range jobs {
				p.attempted.Add(1)
				started := time.Now()
				conn, err := p.clients.dial(p.ctx, index)
				if err != nil {
					p.failure(err)
					continue
				}
				s := &session{conn: conn}
				echoStarted := time.Now()
				if err = verifyEcho(s, p.payload, received); err != nil {
					conn.Close()
					p.failure(err)
					continue
				}
				observe(&p.echoLatency, time.Since(echoStarted))
				observe(&p.connectLatency, time.Since(started))
				s.alive.Store(true)
				p.slots[index].Store(s)
				p.perUser[index%users].Add(1)
				p.active.Add(1)
				p.connected.Add(1)
				p.echoes.Add(1)
				p.verifiedBytes.Add(int64(len(p.payload)))
			}
		}()
	}
	go func() { wg.Wait(); close(done) }()
	return done
}

func (p *probe) heartbeat(period time.Duration) {
	jobs := make(chan *session, 4096)
	for worker := 0; worker < 64; worker++ {
		go func() {
			received := make([]byte, len(p.payload))
			for {
				select {
				case <-p.ctx.Done():
					return
				case s := <-jobs:
					s.mu.Lock()
					if s.alive.Load() {
						started := time.Now()
						if err := verifyEcho(s, p.payload, received); err != nil {
							if s.alive.Swap(false) {
								p.active.Add(-1)
							}
							s.conn.Close()
							p.failure(err)
						} else {
							observe(&p.echoLatency, time.Since(started))
							p.echoes.Add(1)
							p.verifiedBytes.Add(int64(len(p.payload)))
						}
					}
					s.queued.Store(false)
					s.mu.Unlock()
				}
			}
		}()
	}
	go func() {
		ticker := time.NewTicker(period / 64)
		defer ticker.Stop()
		bucket := 0
		for {
			select {
			case <-p.ctx.Done():
				return
			case <-ticker.C:
				if p.quiescing.Load() {
					continue
				}
				for index := bucket; index < len(p.slots); index += 64 {
					s := p.slots[index].Load()
					if s == nil || !s.alive.Load() || !s.queued.CompareAndSwap(false, true) {
						continue
					}
					select {
					case jobs <- s:
					default:
						s.queued.Store(false)
					}
				}
				bucket = (bucket + 1) % 64
			}
		}
	}()
}

func (p *probe) verifyAll() {
	p.quiescing.Store(true)
	jobs := make(chan *session, 128)
	var wg sync.WaitGroup
	for worker := 0; worker < 64; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			received := make([]byte, len(p.payload))
			for s := range jobs {
				s.mu.Lock()
				if !s.alive.Load() {
					p.failure(fmt.Errorf("session closed before final verification"))
				} else if err := verifyEcho(s, p.payload, received); err != nil {
					p.failure(err)
				}
				s.mu.Unlock()
			}
		}()
	}
	for index := range p.slots {
		if s := p.slots[index].Load(); s != nil {
			jobs <- s
		} else {
			p.failure(fmt.Errorf("missing session at final verification"))
		}
	}
	close(jobs)
	wg.Wait()
}

func procNumber(path string) int64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return -1
	}
	value, _ := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	return value
}

func startChild(binary string, args, environment []string, logPath string) (*exec.Cmd, error) {
	file, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(binary, args...)
	cmd.Stdout = file
	cmd.Stderr = file
	cmd.Env = append(os.Environ(), environment...)
	err = cmd.Start()
	file.Close()
	return cmd, err
}

func waitPort(ctx context.Context, address string) error {
	for i := 0; i < 100; i++ {
		c, err := net.DialTimeout("tcp4", address, 100*time.Millisecond)
		if err == nil {
			c.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("listener did not become ready: %s", address)
}

func main() {
	root := flag.String("root", "", "private artifact directory")
	gost := flag.String("gost", "", "gost binary")
	singbox := flag.String("singbox", "/usr/bin/sing-box", "sing-box binary")
	users := flag.Int("users", 10000, "synthetic SOCKS identities")
	protocol := flag.String("protocol", "socks", "socks, vless (TLS/Vision), vmess (AEAD), or mixed")
	perUser := flag.Int("connections-per-user", 10, "connections per identity")
	rate := flag.Int("ramp-per-second", 500, "connection ramp rate")
	hold := flag.Duration("hold", time.Hour, "hold after all connections are ready")
	echoPeriod := flag.Duration("echo-period", 16*time.Second, "per-connection echo period")
	buffer := flag.Int("tcp-buffer", 4096, "per-direction proxy TCP buffer")
	socketBuffer := flag.Int("tcp-socket-buffer", 0, "optional native TCP send/receive buffer bound")
	floor := flag.Int64("memory-floor", 4*1024*1024*1024, "abort below host MemAvailable; staging hosts only")
	flag.Parse()
	if *protocol != "socks" && *protocol != "vless" && *protocol != "vmess" && *protocol != "mixed" {
		panic("unsupported fixture protocol")
	}
	if *root == "" || *gost == "" || *users < 1 || *users > 10000 || *perUser < 1 || *perUser > 10 || *rate < 1 || *rate > 2000 || *echoPeriod < time.Second {
		panic("invalid capacity test configuration")
	}
	// Refuse execution in the host's network namespace.
	self, _ := os.Readlink("/proc/self/ns/net")
	host, _ := os.Readlink("/proc/1/ns/net")
	if self == host {
		panic("capacity probe requires a separate network namespace")
	}
	// A network namespace does not isolate host memory, CPU, swap or global TCP
	// allocations. Refuse machines already running production forwarders.
	processes, err := os.ReadDir("/proc")
	if err != nil {
		panic(err)
	}
	for _, process := range processes {
		if _, err := strconv.Atoi(process.Name()); err != nil {
			continue
		}
		path := filepath.Join("/proc", process.Name())
		comm, err := os.ReadFile(filepath.Join(path, "comm"))
		if err != nil {
			continue
		}
		name := strings.TrimSpace(string(comm))
		if name != "sing-box" && name != "flux-gost" && name != "flux_agent" && name != "gost" {
			continue
		}
		namespace, err := os.Readlink(filepath.Join(path, "ns/net"))
		if err == nil && namespace == host {
			panic("capacity probe refuses a host with active production forwarders")
		}
	}
	if err := os.MkdirAll(*root, 0700); err != nil {
		panic(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	listeners, err := echoListeners(ctx)
	if err != nil {
		panic(err)
	}
	defer func() {
		for _, ln := range listeners {
			ln.Close()
		}
	}()
	var authUsers []map[string]any
	var vlessUsers, vmessUsers []map[string]any
	for index := 0; index < *users; index++ {
		authUsers = append(authUsers, map[string]any{"username": fmt.Sprintf("capacity-%05d", index), "password": "isolated-capacity-fixture-only"})
		vlessUsers = append(vlessUsers, map[string]any{"name": fmt.Sprintf("capacity-%05d", index), "uuid": fixtureUUID(index), "flow": "xtls-rprx-vision"})
		vmessUsers = append(vmessUsers, map[string]any{"name": fmt.Sprintf("capacity-%05d", index), "uuid": fixtureUUID(index), "alterId": 0})
	}
	tlsConfig, err := fixtureTLS(*root)
	if err != nil {
		panic(err)
	}
	clients, err := newProtocolClients(*protocol, *users, tlsConfig)
	if err != nil {
		panic(err)
	}
	authPath := filepath.Join(*root, "singbox-fixture.json")
	authConfig := map[string]any{
		"log": map[string]any{"disabled": true},
		"inbounds": []any{
			map[string]any{"type": "socks", "tag": "capacity-socks", "listen": "127.0.0.1", "listen_port": 25101, "users": authUsers},
			map[string]any{"type": "vless", "tag": "capacity-vless", "listen": "127.0.0.1", "listen_port": 25111, "users": vlessUsers,
				"tls": map[string]any{"enabled": true, "certificate_path": filepath.Join(*root, "fixture-cert.pem"), "key_path": filepath.Join(*root, "fixture-key.pem")}},
			map[string]any{"type": "vmess", "tag": "capacity-vmess", "listen": "127.0.0.1", "listen_port": 25121, "users": vmessUsers},
		},
		"outbounds": []any{map[string]any{"type": "direct", "tag": "echo-only"}},
		"route":     map[string]any{"rules": []any{map[string]any{"ip_cidr": []string{"127.0.0.1/32"}, "port": []int{25200, 25201, 25202, 25203, 25204, 25205, 25206, 25207}, "action": "route", "outbound": "echo-only"}, map[string]any{"action": "reject"}}},
	}
	if err = writeJSON(authPath, authConfig); err != nil {
		panic(err)
	}
	proxyPath := filepath.Join(*root, "gost-fixture.json")
	proxyConfig := map[string]any{"log": map[string]any{"level": "error"},
		"climiters": []any{map[string]any{"name": "total-budget", "limits": []string{"$ 120000"}}},
	}
	var services []any
	for _, base := range []int{25100, 25110, 25120} {
		services = append(services, map[string]any{"name": fmt.Sprintf("isolated-capacity-%d", base), "addr": fmt.Sprintf("127.0.0.1:%d", base),
			"interface": "pool:127.0.0.10,127.0.0.11,127.0.0.12,127.0.0.13,127.0.0.14,127.0.0.15,127.0.0.16,127.0.0.17",
			"climiter":  "total-budget", "handler": map[string]any{"type": "tcp", "metadata": map[string]any{"tcp.bufferSize": *buffer, "tcp.socketBufferSize": *socketBuffer, "writeTimeout": "60s"}},
			"listener": map[string]any{"type": "tcp"}, "forwarder": map[string]any{"nodes": []any{map[string]any{"name": "auth-fixture", "addr": fmt.Sprintf("127.0.0.1:%d", base+1)}}}})
	}
	proxyConfig["services"] = services
	if err = writeJSON(proxyPath, proxyConfig); err != nil {
		panic(err)
	}
	auth, err := startChild(*singbox, []string{"run", "-c", authPath}, []string{"GOMAXPROCS=4"}, filepath.Join(*root, "singbox.log"))
	if err != nil {
		panic(err)
	}
	defer func() { auth.Process.Kill(); auth.Wait() }()
	if err = waitPort(ctx, "127.0.0.1:25101"); err != nil {
		panic(err)
	}
	proxy, err := startChild(*gost, []string{"-standalone", "-C", proxyPath}, []string{"GOMAXPROCS=4", "GOMEMLIMIT=2GiB"}, filepath.Join(*root, "gost.log"))
	if err != nil {
		panic(err)
	}
	defer func() { proxy.Process.Kill(); proxy.Wait() }()
	if err = waitPort(ctx, "127.0.0.1:25100"); err != nil {
		panic(err)
	}
	p := &probe{ctx: ctx, slots: make([]atomic.Pointer[session], *users**perUser), perUser: make([]atomic.Int32, *users), payload: bytes.Repeat([]byte("capacity"), 512), clients: clients}
	defer func() {
		cancel()
		for index := range p.slots {
			if s := p.slots[index].Load(); s != nil {
				s.conn.Close()
			}
		}
	}()
	p.heartbeat(*echoPeriod)
	rampDone := p.ramp(*users, *rate)
	started := time.Now()
	phase := "ramping"
	var holdDone <-chan time.Time
	samples := make([]map[string]any, 0, 400)
	previousBytes := int64(0)
	previousAt := time.Now()
	emit := func(reason string) {
		readyUsers := 0
		min, max := *perUser, 0
		for index := range p.perUser {
			n := int(p.perUser[index].Load())
			if n == *perUser {
				readyUsers++
			}
			if n < min {
				min = n
			}
			if n > max {
				max = n
			}
		}
		sample := map[string]any{"seconds": time.Since(started).Seconds(), "phase": phase, "active": p.active.Load(), "connected": p.connected.Load(), "failures": p.failures.Load(), "echoes": p.echoes.Load(), "verified_bytes": p.verifiedBytes.Load(), "users_complete": readyUsers, "user_connections_min": min, "user_connections_max": max, "host_available": availableMemory(), "proxy": processMemory(proxy.Process.Pid), "singbox": processMemory(auth.Process.Pid), "driver": processMemory(os.Getpid()), "goroutines": runtime.NumGoroutine()}
		sample["conntrack_count"] = procNumber("/proc/sys/net/netfilter/nf_conntrack_count")
		sample["conntrack_max"] = procNumber("/proc/sys/net/netfilter/nf_conntrack_max")
		now := time.Now()
		currentBytes := p.verifiedBytes.Load()
		sample["one_direction_verified_mbps"] = float64(currentBytes-previousBytes) * 8 / time.Since(previousAt).Seconds() / 1e6
		sample["echo_p95_ms_upper"] = percentile(&p.echoLatency, 95)
		sample["echo_p99_ms_upper"] = percentile(&p.echoLatency, 99)
		sample["connect_auth_p99_ms_upper"] = percentile(&p.connectLatency, 99)
		previousAt, previousBytes = now, currentBytes
		if value := p.lastError.Load(); value != nil {
			sample["last_error_type"] = value
		}
		if len(samples) >= 400 {
			samples = samples[1:]
		}
		samples = append(samples, sample)
		report := map[string]any{"target_users": *users, "target_connections": len(p.slots), "tcp_buffer": *buffer, "hold_seconds": hold.Seconds(), "protocol": *protocol, "scope": "isolated network namespace; real SOCKS or VLESS TLS/Vision or VMess AEAD authentication with synthetic identities; no public/WAN traffic", "reason": reason, "samples": samples}
		if e := writeJSON(filepath.Join(*root, "result.json"), report); e != nil {
			fmt.Fprintln(os.Stderr, e)
		}
		out, _ := json.Marshal(sample)
		fmt.Println(string(out))
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	emit("")
	for {
		select {
		case <-ctx.Done():
			phase = "stopped"
			emit("signal")
			return
		case <-rampDone:
			rampDone = nil
			if p.connected.Load() != int64(len(p.slots)) || p.failures.Load() != 0 {
				phase = "failed"
				emit("ramp_failure")
				panic("capacity ramp failed")
			}
			phase = "holding"
			holdDone = time.After(*hold)
			emit("")
		case <-ticker.C:
			if availableMemory() < *floor {
				phase = "safety_abort"
				emit("host_memory_floor")
				panic("host memory guard")
			}
			if len(processMemory(proxy.Process.Pid)) == 0 || len(processMemory(auth.Process.Pid)) == 0 {
				phase = "failed"
				emit("child_exit")
				panic("test child exited")
			}
			if p.failures.Load() != 0 {
				phase = "failed"
				emit("echo_failure")
				panic("capacity echo failed")
			}
			emit("")
		case <-holdDone:
			phase = "final_verification"
			emit("")
			p.verifyAll()
			if p.active.Load() != int64(len(p.slots)) || p.failures.Load() != 0 {
				phase = "failed"
				emit("hold_failure")
				panic("capacity hold failed")
			}
			phase = "passed"
			emit("")
			return
		}
	}
}
