<div align="center">
  <img src="doc/tailgate-logo.jpg" alt="Tailgate" width="200"/>

# Tailgate

[![CI](https://img.shields.io/github/actions/workflow/status/kljensen/tailgate/ci.yml?branch=main&style=for-the-badge&logo=github-actions&logoColor=white&label=CI)](https://github.com/kljensen/tailgate/actions/workflows/ci.yml)
[![License: Unlicense](https://img.shields.io/badge/License-Unlicense-yellow.svg?style=for-the-badge)](https://unlicense.org/)
[![Go Version](https://img.shields.io/github/go-mod/go-version/kljensen/tailgate?style=for-the-badge&logo=go)](https://github.com/kljensen/tailgate)
[![Go Report Card](https://goreportcard.com/badge/github.com/kljensen/tailgate?style=for-the-badge)](https://goreportcard.com/report/github.com/kljensen/tailgate)

A [SOCKS5](https://en.wikipedia.org/wiki/SOCKS#SOCKS5) and
[HTTP CONNECT](https://en.wikipedia.org/wiki/HTTP_tunnel#HTTP_CONNECT_method) proxy
that runs on your [Tailscale](https://tailscale.com) network.

</div>

## Overview

Tailgate is a small, single-binary proxy that joins your Tailscale tailnet
via [tsnet](https://pkg.go.dev/tailscale.com/tsnet) and accepts SOCKS5 and
HTTP CONNECT connections. Unlike a Tailscale
[exit node](https://tailscale.com/kb/1103/exit-nodes) which routes all of a
device's traffic through another node, Tailgate lets you selectively route
only the traffic you choose -- per app, browser, or command.

## Why Tailgate?

- You want to route specific apps or browsers through your tailnet, not all traffic
- You can't (or don't want to) install the Tailscale daemon on the proxy host
- You need a proxy in a container, CI runner, or embedded device with no `/dev/net/tun`
- You want both SOCKS5 and HTTP CONNECT on one port with zero configuration

## Features

- **Automatic protocol detection** -- serves both SOCKS5 and HTTP CONNECT on a single port
- **Joins your tailnet via tsnet** -- no Tailscale daemon required on the proxy host
- **Idle tunnel teardown** -- tunnels with no traffic in either direction are cleaned up automatically
- **Graceful shutdown** -- drains active connections on SIGINT/SIGTERM
- **Hardened request parsing** -- caps CONNECT header size, returns proper 4xx errors

## Installation

```bash
go install github.com/kljensen/tailgate@latest
```

Or build from source (dependencies are vendored):

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
| `-version` | n/a | Print version and exit |

### Starting the proxy

```bash
tailgate -hostname my-proxy -verbose
```

On first run, tsnet will print a login URL. Visit it to authorize the
node on your tailnet. State is saved to `-state-dir` (or a temp
directory by default), so subsequent runs reconnect automatically.

For headless or automated deployments, set `TS_AUTHKEY`:

```bash
TS_AUTHKEY=tskey-auth-... tailgate -hostname my-proxy -state-dir /var/lib/tailgate
```

Generate auth keys at https://login.tailscale.com/admin/settings/keys.
Use `-state-dir` for any persistent deployment so tsnet state survives
reboots.

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
export ALL_PROXY=socks5h://my-proxy:1080

# These then route through Tailgate
curl https://example.com
git clone https://github.com/user/repo.git
wget https://example.com/file.tar.gz
```

### Browsers

[Firefox](https://support.mozilla.org/en-US/kb/connection-settings-firefox)
has built-in proxy settings -- set the SOCKS Host to `my-proxy`, port
`1080`, SOCKS v5, and check "Proxy DNS when using SOCKS v5" in Firefox.
Chrome and Safari use your operating system's proxy settings. See the
[ArchWiki](https://wiki.archlinux.org/title/Proxy_server) for a
comprehensive reference.

## How It Works

Tailgate listens on a single TCP port. When a connection arrives, it peeks
at the first byte: `0x05` means SOCKS5, anything else is parsed as an HTTP
CONNECT request (returning 400 if invalid). Both protocols establish a
bidirectional tunnel to the target host. Each side of the tunnel is wrapped
with an idle timeout so stale connections don't linger forever.

## Security

Tailgate listens via `tsnet.Listen`, so only devices on your Tailscale
network can connect. No proxy authentication is needed -- your tailnet
*is* the trust boundary. Use
[Tailscale ACLs](https://tailscale.com/kb/1018/acls) for finer-grained
access control.

## See Also

- [wireproxy](https://github.com/whyvl/wireproxy) -- the same idea for WireGuard
- [Tailscale userspace networking](https://tailscale.com/kb/1112/userspace-networking) -- Tailscale's built-in SOCKS5 proxy (requires the full daemon)

## License

This is free and unencumbered software released into the public domain.
See [LICENSE](LICENSE) for details.
