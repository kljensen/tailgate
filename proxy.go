package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/things-go/go-socks5"
)

const protocolPeekTimeout = 10 * time.Second
const maxAcceptRetryDelay = 1 * time.Second
const shutdownDrainTimeout = 10 * time.Second

func serve(ctx context.Context, ln net.Listener, logger *slog.Logger) {
	socksServer := socks5.NewServer(
		socks5.WithLogger(&slogSocks5Logger{logger}),
	)
	var retryDelay time.Duration
	var active sync.WaitGroup

	defer func() {
		if !waitForWaitGroup(&active, shutdownDrainTimeout) {
			logger.Warn("graceful shutdown timeout reached", "timeout", shutdownDrainTimeout)
		}
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				slog.Debug("listener closed")
				return
			}
			if isTemporaryAcceptError(err) {
				retryDelay = nextRetryDelay(retryDelay)
				logger.Warn("temporary accept error; retrying", "error", err, "backoff", retryDelay)
				select {
				case <-ctx.Done():
					return
				case <-time.After(retryDelay):
				}
				continue
			}
			slog.Error("accept failed", "error", err)
			return
		}
		retryDelay = 0
		active.Add(1)
		go func() {
			defer active.Done()
			handleConn(conn, socksServer, logger)
		}()
	}
}

func handleConn(conn net.Conn, socksServer *socks5.Server, logger *slog.Logger) {
	defer conn.Close() //nolint:errcheck // best-effort cleanup

	_ = conn.SetReadDeadline(time.Now().Add(protocolPeekTimeout))
	br := bufio.NewReader(conn)
	first, err := br.Peek(1)
	if err != nil {
		slog.Debug("peek failed", "remote", remoteAddr(conn), "error", err)
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	peekConn := &peekedConn{
		Reader: br,
		Conn:   conn,
	}

	if isSOCKS5(first[0]) {
		logger.Debug("routing connection", "remote", remoteAddr(conn), "protocol", "socks5")
		_ = socksServer.ServeConn(peekConn)
		return
	}

	logger.Debug("routing connection", "remote", remoteAddr(conn), "protocol", "http")
	handleHTTPConnect(peekConn, peekConn.Reader, logger)
}

func isSOCKS5(firstByte byte) bool {
	return firstByte == 0x05
}

type peekedConn struct {
	Reader *bufio.Reader
	net.Conn
}

func (c *peekedConn) Read(p []byte) (int, error) {
	return c.Reader.Read(p)
}

func remoteAddr(conn net.Conn) string {
	if conn == nil || conn.RemoteAddr() == nil {
		return ""
	}
	return conn.RemoteAddr().String()
}

// slogSocks5Logger adapts slog.Logger to the socks5.Logger interface.
type slogSocks5Logger struct {
	logger *slog.Logger
}

func (l *slogSocks5Logger) Errorf(format string, args ...any) {
	l.logger.Error(fmt.Sprintf(format, args...))
}

func isTemporaryAcceptError(err error) bool {
	if err == nil {
		return false
	}

	// Common transient error reported by accept under connection churn.
	if errors.Is(err, syscall.ECONNABORTED) {
		return true
	}

	type temporary interface {
		Temporary() bool
	}
	if te, ok := err.(temporary); ok && te.Temporary() {
		return true
	}

	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return true
	}

	return false
}

func nextRetryDelay(current time.Duration) time.Duration {
	if current <= 0 {
		return 50 * time.Millisecond
	}
	next := current * 2
	if next > maxAcceptRetryDelay {
		return maxAcceptRetryDelay
	}
	return next
}

// waitForWaitGroup returns true if wg completes within timeout, false otherwise.
// On timeout the internal goroutine calling wg.Wait is intentionally leaked;
// this is only used during process shutdown where the leak is harmless.
func waitForWaitGroup(wg *sync.WaitGroup, timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}
