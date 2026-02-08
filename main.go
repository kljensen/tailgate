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
		return
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	tsServer := &tsnet.Server{
		Hostname: *hostname,
		Dir:      *stateDir,
	}
	tsServer.Logf = func(format string, args ...any) {
		slog.Debug(fmt.Sprintf("tsnet: "+format, args...))
	}
	defer tsServer.Close() //nolint:errcheck // best-effort cleanup

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	status, err := tsServer.Up(ctx)
	if err != nil {
		slog.Error("failed to bring up tsnet server", "error", err)
		os.Exit(1)
	}
	tailscaleIP := ""
	if len(status.TailscaleIPs) > 0 {
		tailscaleIP = status.TailscaleIPs[0].String()
	}
	slog.Info(
		"tailgate started",
		"hostname", *hostname,
		"tailscale_ip", tailscaleIP,
		"listen", *listen,
		"version", version,
	)

	ln, err := tsServer.Listen("tcp", *listen)
	if err != nil {
		slog.Error("failed to listen", "listen", *listen, "error", err)
		os.Exit(1)
	}
	defer ln.Close() //nolint:errcheck // best-effort cleanup

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	serve(ctx, ln, logger)
}
