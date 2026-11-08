package playhold

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newQuietServer(t *testing.T, cfg *ServerConfig) (*Server, error) {
	t.Helper()
	cfg.RoomType = newTestRoomType(t, RoomConfig{})
	cfg.MemoryLimit = -1
	cfg.Logger = slog.New(slog.DiscardHandler)
	server, err := NewServer(newTestRuntime(t), cfg)
	if err == nil {
		t.Cleanup(server.Close)
	}
	return server, err
}

func TestNewServerRefusesInsecureSettingsOutsideDevelopment(t *testing.T) {
	tests := []struct {
		name    string
		cfg     ServerConfig
		wantErr string
	}{
		{name: "any origin", cfg: ServerConfig{OriginPatterns: []string{"*"}}, wantErr: `"*"`},
		{name: "any origin with a port", cfg: ServerConfig{OriginPatterns: []string{"*:*"}}, wantErr: `"*:*"`},
		{name: "any origin with a dot", cfg: ServerConfig{OriginPatterns: []string{"*.*"}}, wantErr: `"*.*"`},
		{name: "a site's subdomains are fine", cfg: ServerConfig{OriginPatterns: []string{"*.itch.zone"}}},
		{
			name:    "every IPv4 address as a proxy",
			cfg:     ServerConfig{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")}},
			wantErr: "0.0.0.0/0",
		},
		{
			name:    "every IPv6 address as a proxy",
			cfg:     ServerConfig{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("::/0")}},
			wantErr: "::/0",
		},
		{name: "a private network is fine", cfg: ServerConfig{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}},
		{name: "development allows anything", cfg: ServerConfig{OriginPatterns: []string{"*"}, Development: true}},
	}
	for i := range tests {
		tt := &tests[i] // by index: ServerConfig is large
		t.Run(tt.name, func(t *testing.T) {
			_, err := newQuietServer(t, &tt.cfg)

			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestPlainConnectionsAreRefusedOutsideDevelopment(t *testing.T) {
	server, err := newQuietServer(t, &ServerConfig{})
	require.NoError(t, err)
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)

	_, resp, err := websocket.Dial(gatewayContext(t), "ws"+strings.TrimPrefix(httpServer.URL, "http"), nil) //nolint:bodyclose // Dial's docs say the body needs no closing

	require.Error(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestListenAndServeRefusesIncompleteConfigs(t *testing.T) {
	certFile, keyFile, _ := writeTestCertificate(t)
	tests := []struct {
		name    string
		server  ServerConfig
		listen  ListenConfig
		wantErr string
	}{
		{name: "no TLS and no proxy", wantErr: "no TLS and no trusted proxy"},
		{
			name:    "both kinds of certificate",
			listen:  ListenConfig{AutocertDomains: []string{"game.example"}, AutocertCacheDir: t.TempDir(), CertFile: certFile, KeyFile: keyFile},
			wantErr: "not both",
		},
		{
			name:    "automatic certificates without a cache",
			listen:  ListenConfig{AutocertDomains: []string{"game.example"}},
			wantErr: "AutocertCacheDir",
		},
		{name: "a certificate without its key", listen: ListenConfig{CertFile: certFile}, wantErr: "both CertFile and KeyFile"},
		{
			name:    "a certificate file that isn't there",
			listen:  ListenConfig{CertFile: filepath.Join(t.TempDir(), "missing.pem"), KeyFile: keyFile},
			wantErr: "reading the certificate",
		},
	}
	for i := range tests {
		tt := &tests[i] // by index: ServerConfig is large
		t.Run(tt.name, func(t *testing.T) {
			server, err := newQuietServer(t, &tt.server)
			require.NoError(t, err)

			err = server.ListenAndServe(gatewayContext(t), &tt.listen)

			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestAutomaticCertificatesAreSetUpWithoutRequestingOne(t *testing.T) {
	tests := []struct {
		name      string
		directory string
	}{
		{name: "Let's Encrypt"},
		{name: "another directory, such as staging", directory: "https://acme-staging-v02.api.letsencrypt.org/directory"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &ListenConfig{
				AutocertDomains:      []string{"game.example"},
				AutocertCacheDir:     t.TempDir(),
				AutocertDirectoryURL: tt.directory,
			}

			tlsConfig, err := cfg.tlsConfig()

			require.NoError(t, err)
			require.NotNil(t, tlsConfig.GetCertificate, "certificates are requested at the first handshake, not here")
			assert.Contains(t, tlsConfig.NextProtos, "acme-tls/1", "the challenge is answered on the TLS port")
		})
	}
}

func TestServeOverTLS(t *testing.T) {
	certFile, keyFile, roots := writeTestCertificate(t)
	server, err := newQuietServer(t, &ServerConfig{})
	require.NoError(t, err)
	cfg := &ListenConfig{CertFile: certFile, KeyFile: keyFile}
	tlsConfig, err := cfg.tlsConfig()
	require.NoError(t, err)
	var listen net.ListenConfig
	listener, err := listen.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- server.serve(ctx, listener, tlsConfig, cfg) }()

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	base := "https://" + listener.Addr().String()
	ws, _, err := websocket.Dial(gatewayContext(t), "wss"+strings.TrimPrefix(base, "https")+"/ws", //nolint:bodyclose // Dial's docs say the body needs no closing
		&websocket.DialOptions{HTTPClient: client})
	require.NoError(t, err, "a TLS connection is accepted outside development")
	_ = ws.CloseNow()

	req, err := http.NewRequestWithContext(gatewayContext(t), http.MethodGet, base+"/healthz", http.NoBody)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err, "ending ctx shuts down cleanly")
	case <-gatewayContext(t).Done():
		require.FailNow(t, "serve did not return after ctx ended")
	}
}

// writeTestCertificate writes a self-signed certificate for 127.0.0.1 and its key, and returns them with a pool that
// trusts the certificate.
func writeTestCertificate(t *testing.T) (certFile, keyFile string, roots *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)

	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	require.NoError(t, os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	require.NoError(t, os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600))

	parsed, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	roots = x509.NewCertPool()
	roots.AddCert(parsed)
	return certFile, keyFile, roots
}
