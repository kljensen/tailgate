package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	connectReadTimeout     = 15 * time.Second
	connectDialTimeout     = 10 * time.Second
	maxConnectRequestBytes = 8192 // 8KB; generous for CONNECT host:port + headers
)

// tunnelIdleTimeout is the duration with no data in either direction before
// a tunnel is torn down. It is a var so tests can override it.
var tunnelIdleTimeout = 5 * time.Minute

func handleHTTPConnect(conn net.Conn, br *bufio.Reader, logger *slog.Logger) {
	_ = conn.SetReadDeadline(time.Now().Add(connectReadTimeout))
	lr := &io.LimitedReader{R: br, N: maxConnectRequestBytes}
	req, err := http.ReadRequest(bufio.NewReader(lr))
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		status := classifyReadRequestError(lr, err)
		if status == http.StatusRequestHeaderFieldsTooLarge {
			writeHTTPError(conn, http.StatusRequestHeaderFieldsTooLarge, "request too large\n")
		} else {
			writeHTTPError(conn, http.StatusBadRequest, "malformed request\n")
		}
		logger.Debug("failed to read http request", "remote", remoteAddr(conn), "error", err)
		return
	}
	defer req.Body.Close() //nolint:errcheck // best-effort cleanup

	if req.Method != http.MethodConnect {
		writeHTTPError(conn, http.StatusMethodNotAllowed, "CONNECT required\n")
		return
	}

	targetAddr, err := connectTarget(req.Host)
	if err != nil {
		logger.Debug("invalid connect target", "remote", remoteAddr(conn), "host", req.Host, "error", err)
		writeHTTPError(conn, http.StatusBadRequest, "invalid CONNECT host\n")
		return
	}

	target, err := net.DialTimeout("tcp", targetAddr, connectDialTimeout)
	if err != nil {
		logger.Debug("failed to dial target", "target", targetAddr, "error", err)
		writeHTTPError(conn, http.StatusBadGateway, "dial failed\n")
		return
	}
	defer target.Close() //nolint:errcheck // best-effort cleanup

	_, _ = fmt.Fprint(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")

	// Wrap both sides with an idle timeout so tunnels with no traffic
	// in either direction are cleaned up after tunnelIdleTimeout.
	idleConn := &idleTimeoutConn{Conn: conn, timeout: tunnelIdleTimeout}
	idleTarget := &idleTimeoutConn{Conn: target, timeout: tunnelIdleTimeout}

	// Relay bytes bidirectionally. Each goroutine closes the destination
	// when its copy finishes, which unblocks the other goroutine's read.
	// The defers above are safety nets for the redundant close.
	var wg sync.WaitGroup
	wg.Go(func() {
		_, _ = io.Copy(idleTarget, idleConn)
		_ = target.Close()
	})
	wg.Go(func() {
		_, _ = io.Copy(idleConn, idleTarget)
		_ = conn.Close()
	})
	wg.Wait()
}

func connectTarget(hostport string) (string, error) {
	hostport = strings.TrimSpace(hostport)
	if hostport == "" {
		return "", errors.New("empty host")
	}
	if strings.ContainsAny(hostport, " /\\") {
		return "", fmt.Errorf("invalid host format %q", hostport)
	}

	host, port, err := net.SplitHostPort(hostport)
	if err == nil {
		if strings.TrimSpace(host) == "" {
			return "", errors.New("empty host")
		}
		p, err := strconv.ParseUint(port, 10, 16)
		if err != nil || p == 0 {
			return "", fmt.Errorf("invalid port %q", port)
		}
		return net.JoinHostPort(host, port), nil
	}

	if strings.Count(hostport, ":") >= 2 && !strings.HasPrefix(hostport, "[") {
		// Bare IPv6 literal without a port (e.g. "::1"). This heuristic
		// relies on the fact that CONNECT targets are host:port or host,
		// never arbitrary strings with multiple colons.
		return net.JoinHostPort(hostport, "443"), nil
	}

	addrErr := &net.AddrError{}
	if errors.As(err, &addrErr) && strings.Contains(addrErr.Err, "missing port in address") {
		return net.JoinHostPort(hostport, "443"), nil
	}

	return "", err
}

// idleTimeoutConn resets the connection deadline on every Read or Write,
// so the tunnel is torn down if no data flows for the configured duration.
type idleTimeoutConn struct {
	net.Conn
	timeout time.Duration
}

func (c *idleTimeoutConn) Read(p []byte) (int, error) {
	_ = c.SetDeadline(time.Now().Add(c.timeout))
	return c.Conn.Read(p)
}

func (c *idleTimeoutConn) Write(p []byte) (int, error) {
	_ = c.SetDeadline(time.Now().Add(c.timeout))
	return c.Conn.Write(p)
}

func writeHTTPError(conn net.Conn, code int, body string) {
	resp := &http.Response{
		StatusCode:    code,
		ProtoMajor:    1,
		ProtoMinor:    1,
		ContentLength: int64(len(body)),
		Close:         true,
		Body:          io.NopCloser(strings.NewReader(body)),
		Header:        make(http.Header),
	}
	resp.Header.Set("Content-Type", "text/plain; charset=utf-8")
	resp.Header.Set("Connection", "close")
	_ = resp.Write(conn)
}

// classifyReadRequestError returns 431 if the request exceeded the size limit,
// 400 otherwise. The lr.N <= 0 check is reliable because the underlying reader
// is a blocking network stream: bytes are only consumed when actually available,
// so lr.N only reaches zero when maxConnectRequestBytes were truly read.
func classifyReadRequestError(lr *io.LimitedReader, err error) int {
	if lr != nil && lr.N <= 0 {
		return http.StatusRequestHeaderFieldsTooLarge
	}
	if errors.Is(err, bufio.ErrBufferFull) {
		return http.StatusRequestHeaderFieldsTooLarge
	}
	return http.StatusBadRequest
}
