package main

import "testing"

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
