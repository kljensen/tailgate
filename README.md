<div align="center">
  <img src="doc/tailgate-logo.jpg" alt="Tailgate" width="200"/>

# Tailgate

[![License: Unlicense](https://img.shields.io/badge/License-Unlicense-yellow.svg?style=for-the-badge)](https://unlicense.org/)
[![Go Version](https://img.shields.io/github/go-mod-go-version/kljensen/tailgate?style=for-the-badge&logo=go)](https://github.com/kljensen/tailgate)
[![Go Report Card](https://goreportcard.com/badge/github.com/kljensen/tailgate?style=for-the-badge)](https://goreportcard.com/report/github.com/kljensen/tailgate)

A [SOCKS5](https://en.wikipedia.org/wiki/SOCKS#SOCKS5) and
[HTTP CONNECT](https://en.wikipedia.org/wiki/HTTP_tunnel#HTTP_CONNECT_method) proxy
that lives on your [Tailscale](https://tailscale.com) network.

</div>

## Overview

Tailgate is a small, single-binary proxy server that joins your Tailscale
tailnet using [tsnet](https://pkg.go.dev/tailscale.com/tsnet) and accepts
both [SOCKS5](https://en.wikipedia.org/wiki/SOCKS#SOCKS5) and
[HTTP CONNECT](https://en.wikipedia.org/wiki/HTTP_tunnel#HTTP_CONNECT_method)
proxy connections. Point your browser or CLI
tools at it and route traffic through your tailnet without installing
Tailscale on every machine.

## Features

- **SOCKS5 and HTTP CONNECT** in a single listener with automatic protocol detection
- **Embeds into your tailnet** via tsnet -- no Tailscale daemon required on the proxy host
- **Idle tunnel teardown** -- tunnels with no traffic in either direction are cleaned up automatically
- **Graceful shutdown** -- drains active connections on SIGINT/SIGTERM
- **Hardened request parsing** -- caps CONNECT request size, returns proper 4xx errors

## Installation

```bash
go install github.com/kljensen/tailgate@latest
```

Or build from source:

```bash
git clone https://github.com/kljensen/tailgate.git
cd tailgate
go build -mod vendor .
```

## Usage

```
tailgate [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `-hostname` | `tailgate` | Tailscale hostname for this node |
| `-listen` | `:1080` | Address to listen on |
| `-state-dir` | _(tsnet default)_ | Directory for tsnet state |
| `-verbose` | `false` | Enable debug logging |
| `-version` | | Print version and exit |

### Starting the proxy

```bash
tailgate -hostname my-proxy -verbose
```

On first run, tsnet will print a login URL. Authenticate to join the proxy
to your tailnet.

### Proxying with curl

```bash
# Via SOCKS5 (socks5h resolves DNS through the proxy)
curl --proxy socks5h://my-proxy:1080 https://example.com

# Via HTTP CONNECT
curl --proxy http://my-proxy:1080 https://example.com
```

### Environment variables

Many tools (curl, wget, git, python requests, etc.) respect the standard
proxy environment variables:

```bash
export https_proxy=socks5h://my-proxy:1080
export http_proxy=socks5h://my-proxy:1080

# Now these just work
curl https://example.com
git clone https://github.com/user/repo.git
wget https://example.com/file.tar.gz
```

### Browsers

[Firefox](https://support.mozilla.org/en-US/kb/connection-settings-firefox)
has built-in proxy settings -- set the SOCKS Host to `my-proxy`, port
`1080`, SOCKS v5, and check "Proxy DNS when using SOCKS v5". Chrome and
Safari use your operating system's proxy settings. See the
[ArchWiki](https://wiki.archlinux.org/title/Proxy_server) for a
comprehensive reference.

## How It Works

Tailgate listens on a single TCP port. When a connection arrives, it peeks
at the first byte: `0x05` means SOCKS5, anything else is treated as an HTTP
CONNECT request. Both protocols establish a bidirectional tunnel to the
target host. Each side of the tunnel is wrapped with an idle timeout so
stale connections don't linger forever.

## License

This is free and unencumbered software released into the public domain.
See [LICENSE](LICENSE) for details.
