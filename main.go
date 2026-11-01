// scratch-link-go is an unofficial, Linux-only replacement for Scratch Link.
// It speaks the Scratch JSON-RPC protocol over a secure WebSocket and talks
// to Bluetooth Low Energy devices through BlueZ (D-Bus).
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// version is overridden at release time with -ldflags "-X main.version=...".
var version = "dev"

// levelTrace is below slog's Debug; it logs every device notification.
const levelTrace = slog.Level(-8)

type config struct {
	Addr         string
	Host         string
	CertFile     string
	KeyFile      string
	DataDir      string
	Origins      []string
	AnyOrigin    bool
	PollInterval time.Duration
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	var (
		cfg         config
		origins     string
		level       string
		verbose     bool
		showVersion bool
	)
	fs := flag.NewFlagSet("scratch-link-go", flag.ContinueOnError)
	fs.StringVar(&cfg.Addr, "addr", "127.0.0.1:20110", "address to listen on")
	fs.StringVar(&cfg.Host, "host", "device-manager.scratch.mit.edu", "hostname Scratch connects to (must resolve to 127.0.0.1)")
	fs.StringVar(&cfg.CertFile, "cert", "", "TLS certificate (PEM); generated automatically if empty")
	fs.StringVar(&cfg.KeyFile, "key", "", "TLS private key (PEM)")
	fs.StringVar(&cfg.DataDir, "data-dir", "", "where the generated certificate is stored (default ~/.local/share/scratch-link-go)")
	fs.StringVar(&origins, "origins", "", "extra allowed web origins (comma-separated domains), or '*' for any")
	fs.DurationVar(&cfg.PollInterval, "poll", 1500*time.Millisecond, "how often to check that a device is still connected")
	fs.StringVar(&level, "log", "info", "log level: error, warn, info, debug, trace")
	fs.BoolVar(&verbose, "v", false, "shortcut for -log debug")
	fs.BoolVar(&showVersion, "version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if showVersion {
		fmt.Println("scratch-link-go", version)
		return nil
	}
	if verbose {
		level = "debug"
	}
	log, err := newLogger(level)
	if err != nil {
		return err
	}
	if origins == "*" {
		cfg.AnyOrigin = true
	} else {
		for _, o := range strings.Split(origins, ",") {
			if o = strings.ToLower(strings.TrimSpace(o)); o != "" {
				cfg.Origins = append(cfg.Origins, o)
			}
		}
	}

	backend, err := newPlatformBackend(log)
	if err != nil {
		return fmt.Errorf("Bluetooth: %w", err)
	}
	certPath, keyPath, err := ensureCert(cfg, log)
	if err != nil {
		return fmt.Errorf("certificate: %w", err)
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return fmt.Errorf("loading certificate: %w", err)
	}
	if addrs, err := net.LookupHost(cfg.Host); err != nil || !contains(addrs, "127.0.0.1") {
		log.Warn("hostname does not resolve to 127.0.0.1; add this line to /etc/hosts", "line", "127.0.0.1 "+cfg.Host)
	}

	s := newServer(cfg, backend, log)
	srv := &http.Server{
		Addr:     cfg.Addr,
		Handler:  s.handler(),
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelDebug), // TLS handshake noise
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		},
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServeTLS("", "") }()
	log.Info("listening", "url", "https://"+cfg.Addr, "version", version)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
	s.closeAll() // disconnect devices cleanly
	return nil
}

func newLogger(level string) (*slog.Logger, error) {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "error":
		lv = slog.LevelError
	case "warn":
		lv = slog.LevelWarn
	case "info":
		lv = slog.LevelInfo
	case "debug":
		lv = slog.LevelDebug
	case "trace":
		lv = levelTrace
	default:
		return nil, fmt.Errorf("unknown log level %q", level)
	}
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: lv,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.LevelKey && a.Value.Any().(slog.Level) == levelTrace {
				a.Value = slog.StringValue("TRACE")
			}
			return a
		},
	})
	return slog.New(h), nil
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
