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
	connectReadTimeout = 15 * time.Second
	connectDialTimeout = 10 * time.Second
)

func handleHTTPConnect(conn net.Conn, br *bufio.Reader, logger *slog.Logger) {
	_ = conn.SetReadDeadline(time.Now().Add(connectReadTimeout))
	req, err := http.ReadRequest(br)
	if err != nil {
		slog.Debug("failed to read http request", "remote", remoteAddr(conn), "error", err)
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
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

	// Relay bytes bidirectionally. Each goroutine closes the destination
	// when its copy finishes, which unblocks the other goroutine's read.
	// The defers above are safety nets for the redundant close.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(target, conn)
		_ = target.Close()
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(conn, target)
		_ = conn.Close()
	}()
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
