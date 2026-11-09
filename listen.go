package playhold

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

const (
	defaultPath            = "/ws"
	defaultTLSAddr         = ":443"
	defaultPlainAddr       = ":8080"
	defaultMetricsAddr     = "127.0.0.1:9464"
	defaultShutdownTimeout = 8 * time.Second
	readHeaderTimeout      = 5 * time.Second
)

// ListenConfig configures ListenAndServe. Zero values use the defaults. Set either the automatic certificate fields or
// the certificate files, or neither for plain HTTP behind a trusted proxy or in development.
type ListenConfig struct {
	// Addr is the address to listen on. The default is ":443" with TLS and ":8080" without.
	Addr string
	// Path is where clients connect. The default is "/ws". The health checks are at /healthz and /readyz.
	Path string
	// Handler serves every other path, such as the game's page. Nil answers them with 404.
	Handler http.Handler

	// AutocertDomains turns on automatic certificates from Let's Encrypt for these domains, which must point at this
	// server, with port 443 reachable from the internet.
	AutocertDomains []string
	// AutocertCacheDir is the folder that keeps the certificates across restarts, so they aren't requested again each
	// time, which Let's Encrypt limits. Required with AutocertDomains.
	AutocertCacheDir string
	// AutocertEmail is where Let's Encrypt writes about problems with the certificates. Optional.
	AutocertEmail string
	// AutocertDirectoryURL is the certificate authority's ACME directory. Empty uses Let's Encrypt's. Set it to Let's
	// Encrypt's staging directory, https://acme-staging-v02.api.letsencrypt.org/directory, to check a first setup
	// without running into the production limits on failed attempts; browsers don't trust staging certificates. Use a
	// separate AutocertCacheDir for staging, or the cached staging certificate keeps being served afterward.
	AutocertDirectoryURL string

	// CertFile and KeyFile are a certificate and its key, in PEM files, read at start. A renewed certificate takes a
	// restart, which loses no match.
	CertFile, KeyFile string

	// ShutdownTimeout is how long saving the games may take once ctx ends. The default is 8 seconds. Keep it below what
	// the service manager waits before killing the process.
	ShutdownTimeout time.Duration

	// Metrics is where the Prometheus metrics are served, on a listener of their own, never the game's.
	Metrics MetricsListen
}

// MetricsListen says where ListenAndServe serves the metrics, at /metrics. The default address is 127.0.0.1:9464, so
// only the machine itself can read them. Another address, such as the one a container's monitoring scrapes, logs a
// warning outside development, since the metrics show how busy the server is. Off serves none.
type MetricsListen struct {
	Addr string
	Off  bool
}

// ListenAndServe serves the game until ctx ends, then shuts down without losing a match: it saves every game while the
// health checks still answer, and only then stops the HTTP server. With ServerConfig.RestartWhenStuck, it also shuts
// down that way when a restart is needed, and returns ErrRestartNeeded; exit then, for the service manager to start the
// process again. It returns before serving anything if the config is incomplete or, outside development, if there is
// neither TLS nor a trusted proxy.
func (s *Server) ListenAndServe(ctx context.Context, cfg *ListenConfig) error {
	tlsConfig, err := cfg.tlsConfig()
	if err != nil {
		return err
	}
	if tlsConfig == nil && !s.development && !s.proxied {
		return errors.New("playhold: no TLS and no trusted proxy; set the certificate fields, ServerConfig.TrustedProxies, " +
			"or ServerConfig.Development on a developer's machine")
	}
	addr := cfg.Addr
	if addr == "" {
		addr = defaultPlainAddr
		if tlsConfig != nil {
			addr = defaultTLSAddr
		}
	}
	var listen net.ListenConfig
	listener, err := listen.Listen(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	metricsListener, err := s.listenMetrics(ctx, cfg.Metrics)
	if err != nil {
		_ = listener.Close()
		return err
	}
	return s.serve(ctx, listener, tlsConfig, cfg, metricsListener)
}

// listenMetrics opens the metrics' own listener, or returns nil if they're off.
func (s *Server) listenMetrics(ctx context.Context, cfg MetricsListen) (net.Listener, error) {
	if cfg.Off {
		s.logger.Info("metrics off")
		return nil, nil // off isn't an error
	}
	addr := cfg.Addr
	if addr == "" {
		addr = defaultMetricsAddr
	}
	if !s.development && !isLoopback(addr) {
		s.logger.Warn("metrics served on an address other machines can reach; make sure only your monitoring can", "addr", addr)
	}
	var listen net.ListenConfig
	listener, err := listen.Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("playhold: listening for metrics: %w", err)
	}
	return listener, nil
}

// isLoopback reports whether addr only listens on this machine.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

// serve is ListenAndServe on listeners that are already open. A nil metricsListener serves no metrics.
func (s *Server) serve(
	ctx context.Context, listener net.Listener, tlsConfig *tls.Config, cfg *ListenConfig, metricsListener net.Listener,
) error {
	path := cfg.Path
	if path == "" {
		path = defaultPath
	}
	mux := http.NewServeMux()
	mux.Handle(path, s)
	mux.HandleFunc("/healthz", s.Live)
	mux.HandleFunc("/readyz", s.Ready)
	if cfg.Handler != nil {
		mux.Handle("/", cfg.Handler)
	}
	httpServer := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
		TLSConfig:         tlsConfig,
		// TLS handshake failures, including a certificate that couldn't be obtained, go to the server's logs.
		ErrorLog: slog.NewLogLogger(s.logger.Handler(), slog.LevelWarn),
	}

	serving := make(chan error, 1)
	go func() {
		if tlsConfig != nil {
			serving <- httpServer.ServeTLS(listener, "", "") // the certificates come from TLSConfig
		} else {
			serving <- httpServer.Serve(listener)
		}
	}()
	s.logger.Info("listening", "addr", listener.Addr().String(), "tls", tlsConfig != nil, "path", path)
	metricsServer := s.serveMetrics(metricsListener)

	var restart error
	select {
	case err := <-serving:
		if metricsServer != nil {
			_ = metricsServer.Close()
		}
		return err // it stopped before ctx ended, so no player was ever served by it
	case <-ctx.Done():
	case <-s.RestartNeeded():
		restart = ErrRestartNeeded // the same clean shutdown, so the next process restores every room
	}

	timeout := cfg.ShutdownTimeout
	if timeout <= 0 {
		timeout = defaultShutdownTimeout
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	gameErr := s.Shutdown(shutdownCtx)
	httpErr := httpServer.Shutdown(shutdownCtx)
	if err := <-serving; !errors.Is(err, http.ErrServerClosed) {
		httpErr = errors.Join(httpErr, err)
	}
	if metricsServer != nil {
		httpErr = errors.Join(httpErr, metricsServer.Shutdown(shutdownCtx)) // last, so the shutdown itself can be watched
	}
	return errors.Join(restart, gameErr, httpErr)
}

// serveMetrics serves the metrics on their listener until shut down, and returns their HTTP server, or nil without a
// listener.
func (s *Server) serveMetrics(listener net.Listener) *http.Server {
	if listener == nil {
		return nil
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", s.MetricsHandler())
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
		ErrorLog:          slog.NewLogLogger(s.logger.Handler(), slog.LevelWarn),
	}
	go func() {
		if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("serving metrics stopped", "error", err)
		}
	}()
	s.logger.Info("serving metrics", "addr", listener.Addr().String(), "path", "/metrics")
	return server
}

// tlsConfig returns the TLS settings for the chosen mode, or nil for plain HTTP.
func (cfg *ListenConfig) tlsConfig() (*tls.Config, error) {
	automatic := len(cfg.AutocertDomains) > 0
	files := cfg.CertFile != "" || cfg.KeyFile != ""
	switch {
	case automatic && files:
		return nil, errors.New("playhold: set automatic certificates or certificate files, not both")
	case automatic:
		if cfg.AutocertCacheDir == "" {
			return nil, errors.New("playhold: automatic certificates need AutocertCacheDir, so they survive a restart")
		}
		manager := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(cfg.AutocertDomains...),
			Cache:      autocert.DirCache(cfg.AutocertCacheDir),
			Email:      cfg.AutocertEmail,
		}
		if cfg.AutocertDirectoryURL != "" {
			manager.Client = &acme.Client{DirectoryURL: cfg.AutocertDirectoryURL}
		}
		// Answers Let's Encrypt's challenge on the TLS port itself, so nothing has to listen on port 80.
		return manager.TLSConfig(), nil
	case files:
		if cfg.CertFile == "" || cfg.KeyFile == "" {
			return nil, errors.New("playhold: set both CertFile and KeyFile")
		}
		cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("playhold: reading the certificate: %w", err)
		}
		return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, nil
	default:
		return nil, nil // plain HTTP, which ListenAndServe allows only behind a trusted proxy or in development
	}
}
