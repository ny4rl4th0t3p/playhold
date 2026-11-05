package main

import (
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
