package server

import (
	"bufio"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// When TLS is enabled the single listener still accepts plain-HTTP
// connections: the first byte tells a TLS ClientHello (record type 0x16)
// from an HTTP request line, and plain requests get a redirect to the
// https:// URL on the same host and port instead of a handshake error.

const sniffTimeout = 10 * time.Second

// muxListener accepts TCP connections, sniffs the first byte and hands TLS
// connections to Accept while answering plain HTTP with a redirect.
type muxListener struct {
	net.Listener
	tlsConf *tls.Config
	log     *slog.Logger
	conns   chan net.Conn
	errs    chan error
	closed  chan struct{}
}

func newMuxListener(ln net.Listener, conf *tls.Config, log *slog.Logger) *muxListener {
	m := &muxListener{Listener: ln, tlsConf: conf, log: log, conns: make(chan net.Conn), errs: make(chan error, 1), closed: make(chan struct{})}
	go m.loop()
	return m
}

func (m *muxListener) loop() {
	for {
		c, err := m.Listener.Accept()
		if err != nil {
			select {
			case m.errs <- err:
			case <-m.closed:
			}
			return
		}
		go m.dispatch(c)
	}
}

func (m *muxListener) dispatch(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(sniffTimeout))
	br := bufio.NewReaderSize(c, 4096)
	first, err := br.Peek(1)
	if err != nil {
		c.Close()
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	pc := &peekedConn{Conn: c, r: br}
	if first[0] == 0x16 { // TLS handshake record
		select {
		case m.conns <- tls.Server(pc, m.tlsConf):
		case <-m.closed:
			c.Close()
		}
		return
	}
	m.redirect(pc, br)
}

// redirect answers one plain-HTTP request with a redirect to https.
func (m *muxListener) redirect(c net.Conn, br *bufio.Reader) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(sniffTimeout))
	req, err := http.ReadRequest(br)
	if err != nil {
		return // not HTTP either; drop it
	}
	host := req.Host
	if host == "" {
		host = c.LocalAddr().String()
	}
	status := http.StatusPermanentRedirect // keeps the method and body for non-GET clients
	if req.Method == http.MethodGet || req.Method == http.MethodHead {
		status = http.StatusMovedPermanently
	}
	target := "https://" + host + req.URL.RequestURI()
	m.log.Info("redirecting plain http to https", "from", c.RemoteAddr().String(), "method", req.Method, "target", target)
	resp := &http.Response{StatusCode: status, ProtoMajor: 1, ProtoMinor: 1, Header: http.Header{
		"Location": {target}, "Content-Type": {"text/plain; charset=utf-8"}, "Connection": {"close"}}, Body: io.NopCloser(strings.NewReader("use https://\n")), ContentLength: 13}
	_ = resp.Write(c)
}

func (m *muxListener) Accept() (net.Conn, error) {
	select {
	case c := <-m.conns:
		return c, nil
	case err := <-m.errs:
		return nil, err
	case <-m.closed:
		return nil, errors.New("listener closed")
	}
}

func (m *muxListener) Close() error {
	select {
	case <-m.closed:
	default:
		close(m.closed)
	}
	return m.Listener.Close()
}

// peekedConn replays bytes already buffered by the sniff.
type peekedConn struct {
	net.Conn
	r *bufio.Reader
}

func (p *peekedConn) Read(b []byte) (int, error) { return p.r.Read(b) }

// hsts adds Strict-Transport-Security to every response served over TLS.
func hsts(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Strict-Transport-Security", "max-age=63072000")
		next.ServeHTTP(w, r)
	})
}
