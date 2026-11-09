package main

import (
	"flag"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold"
)

func TestParseTimers(t *testing.T) {
	tests := []struct {
		value string
		want  playhold.TimersAfterRestart
	}{
		{value: "pause", want: playhold.TimersPause},
		{value: "keep", want: playhold.TimersKeepRunning},
		{value: "cancel", want: playhold.TimersCancel},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			got, err := parseTimers(tt.value)

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	_, err := parseTimers("stop")
	require.ErrorContains(t, err, "stop")
}

func TestParseProxies(t *testing.T) {
	got, err := parseProxies("10.0.0.0/8,2001:db8::/32")
	require.NoError(t, err)
	assert.Equal(t, []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("2001:db8::/32")}, got)

	got, err = parseProxies("")
	require.NoError(t, err)
	assert.Empty(t, got)

	_, err = parseProxies("10.0.0.2")
	require.ErrorContains(t, err, "-trusted-proxies", "an address without its range length")
}

func TestParseRate(t *testing.T) {
	tests := []struct {
		value   string
		want    playhold.RateLimit
		wantErr bool
	}{
		{value: "", want: playhold.RateLimit{}},
		{value: "off", want: playhold.RateLimit{Off: true}},
		{value: "200ms/20", want: playhold.RateLimit{Every: 200 * time.Millisecond, Burst: 20}},
		{value: "200ms", wantErr: true},
		{value: "fast/20", wantErr: true},
		{value: "0s/20", wantErr: true},
		{value: "1s/0", wantErr: true},
		{value: "1s/many", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			got, err := parseRate(tt.value)

			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseTimeout(t *testing.T) {
	tests := []struct {
		value   string
		want    playhold.Timeout
		wantErr bool
	}{
		{value: "", want: playhold.Timeout{}},
		{value: "off", want: playhold.Timeout{Off: true}},
		{value: "5m", want: playhold.Timeout{After: 5 * time.Minute}},
		{value: "0s", wantErr: true},
		{value: "soon", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			got, err := parseTimeout(tt.value)

			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestConfigFromFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "no flags"},
		{name: "every kind of value", args: []string{
			"-dev", "-connect-rate", "off", "-guest-rate", "6s/20", "-inactive-room-timeout", "1h",
			"-trusted-proxies", "10.0.0.0/8", "-timers-after-restart", "keep", "-debug-addr", ":6060",
		}},
		{name: "a bad rate", args: []string{"-message-rate", "fast"}, wantErr: "fast"},
		{name: "a bad timeout", args: []string{"-disconnect-grace", "soon"}, wantErr: "soon"},
		{name: "a bad proxy", args: []string{"-trusted-proxies", "10.0.0.2"}, wantErr: "-trusted-proxies"},
		{name: "a bad timer policy", args: []string{"-timers-after-restart", "stop"}, wantErr: "stop"},
		{name: "admission off", args: []string{"-admission", "off"}},
		{name: "a crash journal", args: []string{"-crash-journal", "/var/lib/cards/journal", "-crash-journal-entries", "100000"}},
		{name: "admission anything else", args: []string{"-admission", "on"}, wantErr: "-admission"},
		{name: "public profiling outside -dev", args: []string{"-debug-addr", ":6060"}, wantErr: "-debug-addr"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := flag.NewFlagSet("server", flag.ContinueOnError)
			f := defineFlags(fs)
			require.NoError(t, fs.Parse(tt.args))

			cfg, listen, err := f.config()

			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, cfg)
			require.NotNil(t, listen)
		})
	}
}

func TestCheckDebugAddr(t *testing.T) {
	tests := []struct {
		addr    string
		dev     bool
		wantErr bool
	}{
		{addr: "127.0.0.1:6060"},
		{addr: "[::1]:6060"},
		{addr: "localhost:6060"},
		{addr: ":6060", wantErr: true},
		{addr: "0.0.0.0:6060", wantErr: true},
		{addr: "203.0.113.5:6060", wantErr: true},
		{addr: ":6060", dev: true},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			err := checkDebugAddr(tt.addr, tt.dev)

			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}
