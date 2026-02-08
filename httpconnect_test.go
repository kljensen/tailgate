package main

import (
	"bufio"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHandleHTTPConnectTunnel(t *testing.T) {
	t.Parallel()

	targetAddr, stopTarget := startEchoServer(t)
	defer stopTarget()

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close() //nolint:errcheck // test cleanup

	done := make(chan struct{})
	go func() {
		defer close(done)
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		handleHTTPConnect(serverConn, bufio.NewReader(serverConn), logger)
	}()

	req := "CONNECT " + targetAddr + " HTTP/1.1\r\nHost: " + targetAddr + "\r\n\r\n"
	if _, err := io.WriteString(clientConn, req); err != nil {
		t.Fatalf("write connect request: %v", err)
	}

	br := bufio.NewReader(clientConn)
	statusLine, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read status line: %v", err)
	}
	if !strings.Contains(statusLine, "200") {
		t.Fatalf("expected 200 status line, got %q", statusLine)
	}
	// End of headers.
	if _, err := br.ReadString('\n'); err != nil {
		t.Fatalf("read header terminator: %v", err)
	}

	payload := "ping-through-tailgate"
	if _, err := io.WriteString(clientConn, payload); err != nil {
		t.Fatalf("write payload: %v", err)
	}

	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(br, buf); err != nil {
		t.Fatalf("read echoed payload: %v", err)
	}
	if got := string(buf); got != payload {
		t.Fatalf("unexpected echoed payload: got %q want %q", got, payload)
	}

	_ = clientConn.Close()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not exit after client close")
	}
}

func TestHandleHTTPConnectMethodNotAllowed(t *testing.T) {
	t.Parallel()

	statusLine, _ := executeProxyRequest(t, "GET http://example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n")
	if !strings.Contains(statusLine, "405") {
		t.Fatalf("expected 405, got %q", statusLine)
	}
}

func TestHandleHTTPConnectBadTarget(t *testing.T) {
	t.Parallel()

	statusLine, _ := executeProxyRequest(t, "CONNECT example.com:0 HTTP/1.1\r\nHost: example.com:0\r\n\r\n")
	if !strings.Contains(statusLine, "400") {
		t.Fatalf("expected 400, got %q", statusLine)
	}
}

func executeProxyRequest(t *testing.T, request string) (statusLine, body string) {
	t.Helper()

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close() //nolint:errcheck // test cleanup

	done := make(chan struct{})
	go func() {
		defer close(done)
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		handleHTTPConnect(serverConn, bufio.NewReader(serverConn), logger)
	}()

	if _, err := io.WriteString(clientConn, request); err != nil {
		t.Fatalf("write request: %v", err)
	}
	_ = clientConn.SetReadDeadline(time.Now().Add(3 * time.Second))

	br := bufio.NewReader(clientConn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck // test cleanup

	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not exit after request")
	}

	return resp.Status, ""
}

func startEchoServer(t *testing.T) (addr string, stop func()) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("start echo server: %v", err)
	}

	stopCh := make(chan struct{})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-stopCh:
					return
				default:
					return
				}
			}

			go func(c net.Conn) {
				defer c.Close() //nolint:errcheck // test cleanup
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()

	return ln.Addr().String(), func() {
		close(stopCh)
		_ = ln.Close()
	}
}
