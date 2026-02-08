package main

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/things-go/go-socks5"
)

const protocolPeekTimeout = 10 * time.Second

func serve(ln net.Listener, logger *slog.Logger) {
	socksServer := socks5.NewServer(
		socks5.WithLogger(&slogSocks5Logger{logger}),
	)

	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				slog.Debug("listener closed")
				return
			}
			slog.Error("accept failed", "error", err)
			return
		}
		go handleConn(conn, socksServer, logger)
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
