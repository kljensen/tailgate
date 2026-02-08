# Tailgate — A SOCKS5/HTTP proxy that joins your Tailnet

## What this is

Tailgate is a single Go binary that joins a Tailscale network via
[tsnet](https://pkg.go.dev/tailscale.com/tsnet) and serves a combined
SOCKS5 + HTTP CONNECT proxy on a single port. Only devices on your
tailnet can reach it. Traffic exits from the machine where Tailgate
runs, so clients on the tailnet can route requests through a remote
network without using Tailscale's exit node feature.

## Use case

Kyle travels and needs to access Yale network resources. Golf is an
Alpine Linux (arm64) machine on Yale's network, reachable via Tailscale.
Tailgate runs on golf and listens on the tailnet. From the laptop:

```bash
curl --proxy socks5h://tailgate:1080 https://yale-internal-thing.edu
curl --proxy http://tailgate:1080 https://yale-internal-thing.edu
```

Both work. DNS resolution happens on golf's side (important for
Yale-internal hostnames). No SSH tunnels, no exit node, no IP
management.

## Architecture

```
┌─────────────┐         ┌──────────────────────────────────┐
│   Laptop    │         │   Golf (Yale network)            │
│             │  tailnet│                                  │
│  curl_yale ─┼────────>│  tailgate (:1080)                │
│             │         │    ├─ SOCKS5 handler (go-socks5) │
│             │         │    └─ HTTP CONNECT handler       │
│             │         │         │                        │
│             │         │         ▼                        │
│             │         │    net.Dial ──> Yale / Internet   │
└─────────────┘         └──────────────────────────────────┘
```

- **Inbound**: `tsnet.Listen("tcp", ":1080")` — only tailnet devices
  can connect
- **Outbound**: standard `net.Dial` — traffic exits from golf's real
  network (Yale)
- **Mixed mode**: peek at first byte of each connection. `0x05` =
  SOCKS5, otherwise treat as HTTP

## Project structure

```
tailgate/
├── main.go           # Entry point, CLI flags, tsnet setup, listener
├── proxy.go          # Mixed-mode listener: dispatches to socks5 or http
├── httpconnect.go    # HTTP CONNECT proxy handler
├── go.mod
├── go.sum
├── vendor/           # Vendored dependencies (go mod vendor)
├── PLAN.md           # This file
├── README.md         # User-facing docs
└── LICENSE           # MIT
```

Keep it flat — no `cmd/`, `internal/`, `pkg/`. This is a small tool.

## Dependencies

| Dependency | Purpose |
|---|---|
| `tailscale.com/tsnet` | Embed a Tailscale node, get `net.Listener` on the tailnet |
| `github.com/things-go/go-socks5` | SOCKS5 server (maintained fork of `armon/go-socks5`, same API) |

That's it. Use the Go standard library for everything else.

**Do NOT use cobra, logrus, or any other libraries.** This project is
too small. Use `flag` for CLI flags and `log/slog` for structured
logging (stdlib, Go 1.21+).

## CLI interface

```
Usage: tailgate [flags]

Flags:
  -hostname string    Tailscale hostname (default "tailgate")
  -listen string      Port to listen on (default ":1080")
  -state-dir string   tsnet state directory (default "")
  -verbose            Enable verbose/debug logging
  -version            Print version and exit
```

### Version

Embed version at build time via ldflags:

```go
var version = "dev"
```

Build with:

```bash
go build -ldflags="-X main.version=v1.0.0 -s -w" .
```

## Implementation details

### main.go (~80 lines)

```go
package main

import (
    "context"
    "flag"
    "fmt"
    "log/slog"
    "os"
    "os/signal"
    "syscall"

    "tailscale.com/tsnet"
)

var version = "dev"

func main() {
    hostname := flag.String("hostname", "tailgate", "Tailscale hostname")
    listen := flag.String("listen", ":1080", "Port to listen on")
    stateDir := flag.String("state-dir", "", "tsnet state directory")
    verbose := flag.Bool("verbose", false, "Enable verbose logging")
    showVersion := flag.Bool("version", false, "Print version and exit")
    flag.Parse()

    if *showVersion {
        fmt.Println(version)
        os.Exit(0)
    }

    level := slog.LevelInfo
    if *verbose {
        level = slog.LevelDebug
    }
    logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
    slog.SetDefault(logger)

    s := &tsnet.Server{
        Hostname: *hostname,
        Dir:      *stateDir,
    }
    if !*verbose {
        s.Logf = func(string, ...any) {} // suppress tsnet noise
    }
    defer s.Close()

    ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
    defer cancel()

    status, err := s.Up(ctx)
    if err != nil {
        slog.Error("failed to start tsnet", "error", err)
        os.Exit(1)
    }
    slog.Info("tailgate started",
        "hostname", *hostname,
        "tailscale_ip", status.TailscaleIPs[0],
        "listen", *listen,
        "version", version,
    )

    ln, err := s.Listen("tcp", *listen)
    if err != nil {
        slog.Error("failed to listen", "error", err)
        os.Exit(1)
    }
    defer ln.Close()

    // Shut down listener when context is cancelled
    go func() {
        <-ctx.Done()
        ln.Close()
    }()

    serve(ln, logger) // in proxy.go
}
```

### proxy.go (~60 lines)

Mixed-mode dispatcher. Peek at the first byte of each connection to
decide SOCKS5 vs HTTP.

```go
package main

import (
    "bufio"
    "log/slog"
    "net"

    "github.com/things-go/go-socks5"
)

func serve(ln net.Listener, logger *slog.Logger) {
    // Create SOCKS5 server with no auth (tailnet handles access control)
    socksServer, err := socks5.New(
        &socks5.Option{
            Logger: slog.NewLogLogger(logger.Handler(), slog.LevelDebug),
        },
    )
    if err != nil {
        slog.Error("failed to create socks5 server", "error", err)
        return
    }

    for {
        conn, err := ln.Accept()
        if err != nil {
            // Listener closed (shutdown)
            slog.Debug("listener closed", "error", err)
            return
        }
        go handleConn(conn, socksServer, logger)
    }
}

func handleConn(conn net.Conn, socksServer *socks5.Server, logger *slog.Logger) {
    defer conn.Close()

    // Peek at first byte to determine protocol
    br := bufio.NewReader(conn)
    first, err := br.Peek(1)
    if err != nil {
        slog.Debug("peek failed", "error", err, "remote", conn.RemoteAddr())
        return
    }

    // Wrap conn so the peeked byte is not lost
    peekedConn := &peekedConn{Reader: br, Conn: conn}

    if first[0] == 0x05 {
        // SOCKS5
        slog.Debug("socks5 connection", "remote", conn.RemoteAddr())
        socksServer.ServeConn(peekedConn)
    } else {
        // HTTP CONNECT
        slog.Debug("http connection", "remote", conn.RemoteAddr())
        handleHTTPConnect(peekedConn, logger)
    }
}

// peekedConn wraps a buffered reader with the original conn,
// so reads come from the buffer (including peeked bytes) while
// writes go directly to the conn.
type peekedConn struct {
    Reader *bufio.Reader
    net.Conn
}

func (c *peekedConn) Read(b []byte) (int, error) {
    return c.Reader.Read(b)
}
```

### httpconnect.go (~70 lines)

Minimal HTTP CONNECT proxy. No need for full HTTP parsing — just
read the request line, dial, respond 200, relay bytes.

```go
package main

import (
    "fmt"
    "io"
    "log/slog"
    "net"
    "net/http"
    "sync"
)

func handleHTTPConnect(conn net.Conn, logger *slog.Logger) {
    // Read the HTTP request
    req, err := http.ReadRequest(bufio.NewReader(... ))
    // Actually, conn is already a peekedConn with a bufio.Reader.
    // Use http.ReadRequest with a bufio reader from the conn.

    // ... see note below
}
```

**Important implementation note**: Since `peekedConn` already wraps a
`bufio.Reader`, and `http.ReadRequest` wants a `*bufio.Reader`, you
should construct the `*bufio.Reader` carefully to avoid double-buffering.
The simplest approach: in `handleHTTPConnect`, create a new
`bufio.Reader` from the `peekedConn` (which reads from the existing
buffer). `http.ReadRequest` handles parsing.

Here's the actual logic:

```go
func handleHTTPConnect(conn net.Conn, logger *slog.Logger) {
    br := bufio.NewReader(conn)
    req, err := http.ReadRequest(br)
    if err != nil {
        slog.Debug("failed to read http request", "error", err)
        return
    }

    if req.Method != http.MethodConnect {
        resp := &http.Response{
            StatusCode: http.StatusMethodNotAllowed,
            ProtoMajor: 1,
            ProtoMinor: 1,
        }
        resp.Write(conn)
        return
    }

    // Dial the target
    target, err := net.Dial("tcp", req.Host)
    if err != nil {
        slog.Debug("failed to dial target", "host", req.Host, "error", err)
        fmt.Fprintf(conn, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
        return
    }
    defer target.Close()

    // Send 200 to client
    fmt.Fprintf(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")

    // Relay bytes bidirectionally
    var wg sync.WaitGroup
    wg.Add(2)
    go func() {
        defer wg.Done()
        io.Copy(target, conn)
    }()
    go func() {
        defer wg.Done()
        io.Copy(conn, target)
    }()
    wg.Wait()
}
```

That's the entire HTTP CONNECT proxy. ~40 lines of real logic.

**Note on double-buffering**: `peekedConn.Read()` already reads from
a `bufio.Reader`. Creating another `bufio.NewReader(conn)` in
`handleHTTPConnect` adds a second buffer on top, which is fine — it
just means a small extra allocation. If you want to avoid it, pass
the `*bufio.Reader` directly instead of going through `peekedConn`.
Either works; don't over-optimize this.

## Build

```bash
# Development
go build .

# Release (static, stripped, versioned)
CGO_ENABLED=0 go build -ldflags="-X main.version=v1.0.0 -s -w" .

# Vendor dependencies
go mod vendor
```

Target: `GOOS=linux GOARCH=arm64` for golf.

## First run

On first run, tsnet needs to authenticate with Tailscale. Two ways:

1. **Interactive**: Run `tailgate` and it prints a login URL. Visit
   it to authorize the node.
2. **Auth key**: Set `TS_AUTHKEY=tskey-auth-...` environment variable.
   The node registers automatically. Used for Ansible deployment.

After first auth, state is persisted to `--state-dir` and subsequent
starts are automatic.

## Testing

Manual testing is sufficient for a tool this size:

```bash
# Start tailgate locally (it joins your tailnet)
TS_AUTHKEY=... go run . -hostname tailgate-test -verbose

# From another tailnet device:
curl --proxy socks5h://tailgate-test:1080 https://httpbin.org/ip
curl --proxy http://tailgate-test:1080 https://httpbin.org/ip

# Both should return the IP of the machine running tailgate
```

You can also write a simple Go test that:
1. Starts a local TCP echo server
2. Creates a `peekedConn` with a SOCKS5 or HTTP CONNECT prefix
3. Verifies the dispatcher routes correctly

But the tsnet integration itself can only be tested on a real tailnet.

## Deployment to golf (via home-provisioning Ansible)

This will be a new Ansible role `alpine-tailgate` in the
home-provisioning repo, following the exact `alpine-ldap-api` pattern.

### Ansible role: `alpine-tailgate`

```
roles/alpine-tailgate/
├── defaults/main.yml
├── tasks/main.yml
├── templates/
│   └── supervisord.ini.j2
└── handlers/main.yml
```

#### defaults/main.yml

```yaml
---
tailgate_version: "v1.0.0"
tailgate_user: "tailgate"
tailgate_group: "tailgate"
tailgate_state_dir: "/var/lib/tailgate"
tailgate_app_dir: "{{ tailgate_state_dir }}/app"
tailgate_bin_path: "{{ tailgate_app_dir }}/tailgate"
tailgate_hostname: "tailgate"
tailgate_listen: ":1080"
tailgate_authkey: "{{ tailgate_tailscale_authkey }}"  # from vault
```

#### tasks/main.yml

```yaml
---
- name: Create tailgate system group
  ansible.builtin.group:
    name: "{{ tailgate_group }}"
    system: true

- name: Create tailgate system user
  ansible.builtin.user:
    name: "{{ tailgate_user }}"
    group: "{{ tailgate_group }}"
    system: true
    shell: /sbin/nologin
    home: "{{ tailgate_state_dir }}"
    create_home: true

- name: Create app directory
  ansible.builtin.file:
    path: "{{ tailgate_app_dir }}"
    state: directory
    owner: "{{ tailgate_user }}"
    group: "{{ tailgate_group }}"
    mode: '0755'

- name: Create tsnet state directory
  ansible.builtin.file:
    path: "{{ tailgate_state_dir }}/tsnet"
    state: directory
    owner: "{{ tailgate_user }}"
    group: "{{ tailgate_group }}"
    mode: '0700'

- name: Get GitHub token from local machine
  ansible.builtin.command: gh auth token
  delegate_to: localhost
  vars:
    ansible_become: false
  register: github_token
  changed_when: false

- name: Check current tailgate version
  ansible.builtin.shell: "{{ tailgate_bin_path }} -version 2>/dev/null || echo 'not installed'"
  register: tailgate_current_version
  changed_when: false
  failed_when: false

- name: Install tailgate via go install
  ansible.builtin.shell: |
    CGO_ENABLED=0 GOBIN={{ tailgate_app_dir }} \
      GONOSUMCHECK=github.com/kljensen/tailgate \
      go install -ldflags="-X main.version={{ tailgate_version }} -s -w" \
      github.com/kljensen/tailgate@{{ tailgate_version }}
  environment:
    PATH: "/usr/local/go/bin:/usr/bin:/bin"
    GIT_CONFIG_COUNT: "1"
    GIT_CONFIG_KEY_0: "url.https://{{ github_token.stdout }}@github.com/.insteadOf"
    GIT_CONFIG_VALUE_0: "https://github.com/"
  become: true
  become_user: "{{ tailgate_user }}"
  when: tailgate_current_version.stdout.find(tailgate_version) == -1
  notify: restart tailgate

# Bootstrap: register with Tailscale if no state exists yet
- name: Check if tsnet state exists
  ansible.builtin.stat:
    path: "{{ tailgate_state_dir }}/tsnet/tailscaled.state"
  register: tsnet_state

- name: Bootstrap tsnet registration
  ansible.builtin.shell: |
    timeout 30 {{ tailgate_bin_path }} \
      -hostname={{ tailgate_hostname }} \
      -listen={{ tailgate_listen }} \
      -state-dir={{ tailgate_state_dir }}/tsnet || true
  environment:
    TS_AUTHKEY: "{{ tailgate_authkey }}"
  become: true
  become_user: "{{ tailgate_user }}"
  when: not tsnet_state.stat.exists

- name: Deploy supervisord config
  ansible.builtin.template:
    src: supervisord.ini.j2
    dest: /etc/supervisor.d/tailgate.ini
    owner: root
    group: root
    mode: '0644'
  notify: reload supervisord for tailgate

- name: Ensure tailgate is running
  ansible.builtin.shell: |
    supervisorctl reread
    supervisorctl update
    supervisorctl start tailgate || true
  register: tailgate_start
  changed_when: "'started' in tailgate_start.stdout or 'updated' in tailgate_start.stdout"
```

#### templates/supervisord.ini.j2

```ini
[program:tailgate]
command={{ tailgate_bin_path }} -hostname={{ tailgate_hostname }} -listen={{ tailgate_listen }} -state-dir={{ tailgate_state_dir }}/tsnet
directory={{ tailgate_app_dir }}
user={{ tailgate_user }}
environment=HOME="{{ tailgate_state_dir }}",PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
autostart=true
autorestart=true
startsecs=10
startretries=3
stopwaitsecs=30
stdout_logfile=/var/log/supervisord/tailgate.stdout.log
stderr_logfile=/var/log/supervisord/tailgate.stderr.log
stdout_logfile_maxbytes=10MB
stderr_logfile_maxbytes=10MB
```

#### handlers/main.yml

```yaml
---
- name: restart tailgate
  ansible.builtin.shell: |
    supervisorctl stop tailgate || true
    supervisorctl start tailgate
  become: true

- name: reload supervisord for tailgate
  ansible.builtin.shell: |
    supervisorctl reread
    supervisorctl update tailgate
  become: true
```

#### Add to playbook (playbooks/default.yaml)

Insert after `alpine-supervisord`, before the app roles:

```yaml
    - import_role:
        name: alpine-tailgate
      tags: ["tailgate", "proxy"]
      vars:
        ansible_become: true
```

#### Add to vault (host_vars/golf/vault.yaml)

```yaml
tailgate_tailscale_authkey: "tskey-auth-..."
```

### Updated curl_yale (roles/laptop/files/bin/curl_yale)

Replace the entire SSH tunnel script with:

```bash
#!/usr/bin/env bash
set -euo pipefail

PROXY_HOST="tailgate"
PROXY_PORT=1080

# Prefer curl_chrome142 (curl-impersonate) for browser-like TLS fingerprint,
# fall back to plain curl
if command -v curl_chrome142 &>/dev/null; then
    CURL_CMD=curl_chrome142
else
    CURL_CMD=curl
fi

case "${1:-}" in
    --help)
        echo "Usage: curl_yale [--help] [curl args...]"
        echo ""
        echo "Curls through tailgate proxy on golf (Yale network)."
        echo "Uses curl_chrome142 if available, otherwise plain curl."
        exit 0
        ;;
esac

exec "${CURL_CMD}" --proxy "socks5h://${PROXY_HOST}:${PROXY_PORT}" "$@"
```

Note: `PROXY_HOST` is now `tailgate` (the tsnet hostname), not `golf`.
Tailscale MagicDNS resolves it.

## Key design decisions

| Decision | Rationale |
|---|---|
| tsnet (not bind to tailscale IP) | No IP management. tsnet listener is only reachable from tailnet by definition. |
| Mixed mode (one port) | Simpler for clients. Peek at first byte: 0x05 = SOCKS5, else HTTP. |
| `net.Dial` for outbound | Traffic exits from golf's real network (Yale), not through tailscale. This is the whole point. |
| No auth on the proxy | Tailnet IS the auth boundary. Only tailnet devices can connect. |
| `things-go/go-socks5` | Maintained fork of `armon/go-socks5`. Same API, active maintenance. Accepts any `net.Listener` via `Serve()`. |
| `socks5h://` in curl_yale | The `h` means DNS resolution happens on the proxy side (golf/Yale network). Critical for Yale-internal hostnames. |
| `go install` deployment | Same pattern as ldap-api. Go toolchain already on golf. Version-pinned. |
| Flat project structure | ~200 lines total. No need for packages. |
| `flag` not `cobra`, `slog` not `logrus` | Too small for frameworks. stdlib is fine. |
| MIT license | User wants this potentially useful to others. |

## Checklist for implementation

1. [ ] `go mod init github.com/kljensen/tailgate`
2. [ ] `go get tailscale.com/tsnet`
3. [ ] `go get github.com/things-go/go-socks5`
4. [ ] Write `main.go` — flags, tsnet setup, listener, signal handling
5. [ ] Write `proxy.go` — mixed-mode dispatcher, peekedConn type
6. [ ] Write `httpconnect.go` — HTTP CONNECT handler
7. [ ] `go mod vendor`
8. [ ] Test: `go run . -hostname tailgate-test -verbose` + curl through it
9. [ ] Write `README.md`
10. [ ] Add `LICENSE` (MIT)
11. [ ] Tag `v1.0.0`
12. [ ] In home-provisioning: create `roles/alpine-tailgate/`
13. [ ] In home-provisioning: update `playbooks/default.yaml`
14. [ ] In home-provisioning: update `roles/laptop/files/bin/curl_yale`
15. [ ] In home-provisioning: add auth key to vault
16. [ ] Deploy: `ansible-playbook playbooks/default.yaml -l golf -t tailgate`
17. [ ] Test from laptop: `curl_yale https://httpbin.org/ip`
