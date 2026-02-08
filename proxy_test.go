package main

import (
	"errors"
	"net"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestIsSOCKS5(t *testing.T) {
	t.Parallel()

	if !isSOCKS5(0x05) {
		t.Fatalf("expected 0x05 to be detected as socks5")
	}
	if isSOCKS5('C') {
		t.Fatalf("expected non-0x05 byte to be treated as non-socks")
	}
}

func TestConnectTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{name: "host_with_port", in: "example.com:8443", want: "example.com:8443", ok: true},
		{name: "host_no_port", in: "example.com", want: "example.com:443", ok: true},
		{name: "ipv4_no_port", in: "127.0.0.1", want: "127.0.0.1:443", ok: true},
		{name: "ipv6_no_port", in: "::1", want: "[::1]:443", ok: true},
		{name: "empty", in: "", ok: false},
		{name: "invalid_port", in: "example.com:abc", ok: false},
		{name: "contains_path", in: "example.com/path", ok: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := connectTarget(tc.in)
			if tc.ok && err != nil {
				t.Fatalf("connectTarget(%q) unexpected err: %v", tc.in, err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("connectTarget(%q) expected error", tc.in)
			}
			if tc.ok && got != tc.want {
				t.Fatalf("connectTarget(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestIsTemporaryAcceptError(t *testing.T) {
	t.Parallel()

	if !isTemporaryAcceptError(syscall.ECONNABORTED) {
		t.Fatal("expected ECONNABORTED to be temporary")
	}

	timeoutErr := &stubNetError{timeout: true}
	if !isTemporaryAcceptError(timeoutErr) {
		t.Fatal("expected timeout net.Error to be temporary")
	}

	temporaryErr := &stubNetError{temporary: true}
	if !isTemporaryAcceptError(temporaryErr) {
		t.Fatal("expected Temporary() error to be temporary")
	}

	if isTemporaryAcceptError(errors.New("permanent")) {
		t.Fatal("expected plain error to be non-temporary")
	}
}

func TestNextRetryDelay(t *testing.T) {
	t.Parallel()

	if got := nextRetryDelay(0); got != 50*time.Millisecond {
		t.Fatalf("nextRetryDelay(0) = %v, want 50ms", got)
	}
	if got := nextRetryDelay(50 * time.Millisecond); got != 100*time.Millisecond {
		t.Fatalf("nextRetryDelay(50ms) = %v, want 100ms", got)
	}
	if got := nextRetryDelay(maxAcceptRetryDelay); got != maxAcceptRetryDelay {
		t.Fatalf("nextRetryDelay(max) = %v, want %v", got, maxAcceptRetryDelay)
	}
}

func TestWaitForWaitGroup(t *testing.T) {
	t.Parallel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(20 * time.Millisecond)
	}()

	if !waitForWaitGroup(&wg, 200*time.Millisecond) {
		t.Fatal("expected waitForWaitGroup to complete before timeout")
	}
}

func TestWaitForWaitGroupTimeout(t *testing.T) {
	t.Parallel()

	var wg sync.WaitGroup
	wg.Add(1)
	defer wg.Done()

	if waitForWaitGroup(&wg, 10*time.Millisecond) {
		t.Fatal("expected waitForWaitGroup timeout")
	}
}

type stubNetError struct {
	timeout   bool
	temporary bool
}

func (e *stubNetError) Error() string   { return "stub" }
func (e *stubNetError) Timeout() bool   { return e.timeout }
func (e *stubNetError) Temporary() bool { return e.temporary }

var _ net.Error = (*stubNetError)(nil)
