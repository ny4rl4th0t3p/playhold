package main

import (
	"net/netip"
	"testing"

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
