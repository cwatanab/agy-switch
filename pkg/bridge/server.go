package bridge

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/proxy"
	xproxy "golang.org/x/net/proxy"
)

// ServerOptions controls connection setup and idle tunnel deadlines.
type ServerOptions struct {
	DialTimeout       time.Duration
	TunnelIdleTimeout time.Duration
}

type Server struct {
	paths      *config.Paths
	upstream   *proxy.Config
	managed    bool
	port       int
	token      string
	listener   net.Listener
	httpServer *http.Server
	lock       *Lock
	ownsLock   bool
	transport  *http.Transport
	stopped    atomic.Bool
	active     atomic.Int64
	lastReq    atomic.Int64
	mu         sync.Mutex
	conns      map[net.Conn]struct{}
	ctx        context.Context
	cancel     context.CancelFunc
	opts       ServerOptions
}

func NewServer(paths *config.Paths, upstream *proxy.Config, port int) (*Server, error) {
	return NewServerWithOptions(paths, upstream, port, ServerOptions{})
}

// NewServerWithOptions creates a bridge with bounded dialing and tunnel idleness.
func NewServerWithOptions(paths *config.Paths, upstream *proxy.Config, port int, opts ServerOptions) (*Server, error) {
	token := ""
	if st, err := LoadState(paths); err == nil {
		token = st.Token
		if port == 0 {
			port = st.Port
		}
	}
	if token == "" {
		var err error
		token, err = GenerateToken()
		if err != nil {
			return nil, err
		}
	}
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = 10 * time.Second
	}
	if opts.TunnelIdleTimeout <= 0 {
		opts.TunnelIdleTimeout = 5 * time.Minute
	}
	lockPath := paths.BridgeLock
	if lockPath == "" {
		lockPath = paths.DataDir + "/bridge.lock"
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{paths: paths, port: port, token: token, lock: NewLock(lockPath), conns: make(map[net.Conn]struct{}), ctx: ctx, cancel: cancel, opts: opts}
	if upstream != nil {
		cfg := *upstream
		s.upstream = &cfg
	}
	if cfg, err := proxy.NewStore(paths).Active(); err != nil {
		cancel()
		return nil, err
	} else {
		s.managed = cfg != nil
	}
	s.lastReq.Store(time.Now().Unix())
	s.transport = &http.Transport{DialContext: s.dialContext, MaxIdleConns: 50, IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: 30 * time.Second}
	return s, nil
}

func (s *Server) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return 0
	}
	return s.port
}

func (s *Server) Token() string { return s.token }

// ActiveConnections counts the owned endpoints of hijacked CONNECT tunnels.
func (s *Server) ActiveConnections() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.conns) }

func (s *Server) refreshUpstream() error {
	if !s.managed {
		return nil
	}
	store := proxy.NewStore(s.paths)
	cfg, err := store.Active()
	if err != nil {
		return err
	}
	s.mu.Lock()
	previous := s.upstream
	s.mu.Unlock()
	if cfg == nil || (cfg.Protocol != "socks5" && cfg.Protocol != "socks5h") {
		if previous == nil {
			return fmt.Errorf("no SOCKS upstream available")
		}
		// Existing sessions can keep using the last SOCKS entry after routing is off.
		cfg, err = store.Get(previous.Name)
		if err != nil {
			return err
		}
	}
	if cfg.Protocol != "socks5" && cfg.Protocol != "socks5h" {
		return fmt.Errorf("upstream is no longer a SOCKS proxy")
	}
	s.mu.Lock()
	changed := s.upstream == nil || *s.upstream != *cfg
	s.upstream = cfg
	s.mu.Unlock()
	if changed {
		s.transport.CloseIdleConnections()
	}
	return nil
}

type captureDialer struct {
	net.Dialer
	conn net.Conn
}

func (d *captureDialer) Dial(network, addr string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, addr)
}
func (d *captureDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, err := d.Dialer.DialContext(ctx, network, addr)
	d.conn = conn
	return conn, err
}

type halfCloseConn struct {
	net.Conn
	raw net.Conn
}

func (c *halfCloseConn) CloseWrite() error {
	if conn, ok := c.raw.(interface{ CloseWrite() error }); ok {
		return conn.CloseWrite()
	}
	return c.raw.Close()
}

func (s *Server) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, s.opts.DialTimeout)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	if err := s.refreshUpstream(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	cfg := s.upstream
	s.mu.Unlock()
	forward := &captureDialer{Dialer: net.Dialer{Timeout: s.opts.DialTimeout, KeepAlive: 30 * time.Second}}
	if cfg == nil {
		return forward.DialContext(ctx, network, addr)
	}
	if cfg.Protocol != "socks5" && cfg.Protocol != "socks5h" {
		return nil, fmt.Errorf("unsupported bridge upstream protocol")
	}
	var auth *xproxy.Auth
	if cfg.Username != "" {
		auth = &xproxy.Auth{User: cfg.Username, Password: cfg.Password}
	}
	dialer, err := xproxy.SOCKS5("tcp", net.JoinHostPort(cfg.GetServer(), fmt.Sprint(cfg.Port)), auth, forward)
	if err != nil {
		return nil, err
	}
	contextual, ok := dialer.(xproxy.ContextDialer)
	if !ok {
		return nil, fmt.Errorf("SOCKS dialer does not support cancellation")
	}
	conn, err := contextual.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	// x/net's SOCKS wrapper does not expose TCP half-close; retain its raw socket.
	return &halfCloseConn{Conn: conn, raw: forward.conn}, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !ValidateAuth(r, s.token) {
		w.Header().Set("Proxy-Authenticate", `Basic realm="agy-switch"`)
		w.WriteHeader(http.StatusProxyAuthRequired)
		return
	}
	if r.URL.Host == "agy-switch.bridge" || r.Host == "agy-switch.bridge" {
		stop := r.URL.Path == "/stop" && r.Method == http.MethodPost
		if !stop && !(r.URL.Path == "/ping" && r.Method == http.MethodGet) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "agy-switch-bridge-ok\n")
		if stop {
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			go s.Stop()
		}
		return
	}
	if err := s.refreshUpstream(); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.active.Add(1)
	s.lastReq.Store(time.Now().Unix())
	defer func() { s.lastReq.Store(time.Now().Unix()); s.active.Add(-1) }()
	if r.Method == http.MethodConnect {
		s.handleConnect(w, r)
		return
	}
	s.handleHTTP(w, r)
}

var bufferPool = sync.Pool{New: func() interface{} { b := make([]byte, 32*1024); return &b }}

type idleConn struct {
	net.Conn
	timeout time.Duration
}

func (c *idleConn) Read(p []byte) (int, error) {
	if err := c.SetReadDeadline(time.Now().Add(c.timeout)); err != nil {
		return 0, err
	}
	return c.Conn.Read(p)
}
func (c *idleConn) Write(p []byte) (int, error) {
	if err := c.SetWriteDeadline(time.Now().Add(c.timeout)); err != nil {
		return 0, err
	}
	return c.Conn.Write(p)
}
func (c *idleConn) CloseWrite() error {
	if conn, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return conn.CloseWrite()
	}
	return c.Conn.Close()
}

func (s *Server) track(conn net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped.Load() {
		_ = conn.Close()
		return false
	}
	s.conns[conn] = struct{}{}
	return true
}
func (s *Server) untrack(conn net.Conn) {
	_ = conn.Close()
	s.mu.Lock()
	delete(s.conns, conn)
	s.mu.Unlock()
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	dest, err := s.dialContext(r.Context(), "tcp", r.Host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if !s.track(dest) {
		return
	}
	defer s.untrack(dest)
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}
	client, buffered, err := hijacker.Hijack()
	if err != nil {
		return
	}
	if !s.track(client) {
		return
	}
	defer s.untrack(client)
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	source := &idleConn{Conn: client, timeout: s.opts.TunnelIdleTimeout}
	target := &idleConn{Conn: dest, timeout: s.opts.TunnelIdleTimeout}
	// The buffered reader can already contain bytes sent alongside CONNECT.
	var reader io.Reader = source
	if buffered.Reader.Buffered() != 0 {
		reader = io.MultiReader(io.LimitReader(buffered.Reader, int64(buffered.Reader.Buffered())), source)
	}
	var wg sync.WaitGroup
	copyHalf := func(dst *idleConn, src io.Reader) {
		defer wg.Done()
		buf := bufferPool.Get().(*[]byte)
		defer bufferPool.Put(buf)
		if _, err := io.CopyBuffer(dst, src, *buf); err != nil {
			_ = source.Close()
			_ = target.Close()
			return
		}
		_ = dst.CloseWrite()
	}
	wg.Add(2)
	go copyHalf(target, reader)
	go copyHalf(source, target)
	wg.Wait()
}

func splitHeaderTokens(value string) []string {
	tokens := strings.Split(value, ",")
	for i := range tokens {
		tokens[i] = strings.TrimSpace(tokens[i])
	}
	return tokens
}

func stripHopHeaders(h http.Header) {
	for _, token := range h.Values("Connection") {
		// Connection names additional hop-by-hop fields that must not be forwarded.
		for _, name := range splitHeaderTokens(token) {
			h.Del(name)
		}
	}
	for _, name := range []string{"Connection", "Proxy-Authorization", "Proxy-Authenticate", "Proxy-Connection", "Keep-Alive", "TE", "Trailer", "Transfer-Encoding", "Upgrade"} {
		h.Del(name)
	}
}

func (s *Server) handleHTTP(w http.ResponseWriter, r *http.Request) {
	out := r.Clone(r.Context())
	out.RequestURI = ""
	stripHopHeaders(out.Header)
	resp, err := s.transport.RoundTrip(out)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	stripHopHeaders(resp.Header)
	for name, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (s *Server) startWatchdog() {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				if s.active.Load() == 0 && time.Now().Unix()-s.lastReq.Load() > 300 {
					if count, err := CountAgyProcesses(); err == nil && count == 0 {
						_ = s.Stop()
						return
					}
				}
			}
		}
	}()
}

func (s *Server) Start() error {
	acquired, err := s.lock.TryLock()
	if err != nil {
		return err
	}
	if !acquired {
		return fmt.Errorf("another bridge is running")
	}
	s.mu.Lock()
	s.ownsLock = true
	s.mu.Unlock()
	defer s.Stop()
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.port))
	if err != nil {
		return err
	}
	server := &http.Server{Handler: s, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 * 1024}
	s.mu.Lock()
	if s.stopped.Load() {
		s.mu.Unlock()
		_ = listener.Close()
		return net.ErrClosed
	}
	s.listener, s.httpServer = listener, server
	s.port = listener.Addr().(*net.TCPAddr).Port
	port := s.port
	err = SaveState(s.paths, &State{Port: port, Token: s.token, PID: os.Getpid()})
	s.mu.Unlock()
	if err != nil {
		return err
	}
	s.startWatchdog()
	return server.Serve(listener)
}

func (s *Server) Stop() error {
	if !s.stopped.CompareAndSwap(false, true) {
		return nil
	}
	s.cancel()
	s.mu.Lock()
	listener, server, owns := s.listener, s.httpServer, s.ownsLock
	for conn := range s.conns {
		_ = conn.Close()
	}
	s.mu.Unlock()
	if server != nil {
		_ = server.Close()
	}
	if listener != nil {
		_ = listener.Close()
	}
	s.transport.CloseIdleConnections()
	if owns {
		RemoveState(s.paths)
		return s.lock.Unlock()
	}
	return nil
}
