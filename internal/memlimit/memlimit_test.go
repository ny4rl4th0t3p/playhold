package memlimit

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
)

func TestDefault(t *testing.T) {
	const (
		meminfo = "MemFree:  1 kB\nMemTotal:  1000 kB\nSwapTotal:  5 kB\n"
		machine = 1000 * 1024
	)
	file := func(data string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(data)} }

	tests := []struct {
		name  string
		files fstest.MapFS
		want  int64
	}{
		{name: "nothing to read", files: fstest.MapFS{}, want: 0},
		{name: "machine only", files: fstest.MapFS{"proc/meminfo": file(meminfo)}, want: machine / 100 * 80},
		{
			name: "cgroup v2 limit below the machine",
			files: fstest.MapFS{
				"proc/meminfo":             file(meminfo),
				"sys/fs/cgroup/memory.max": file("512000\n"),
			},
			want: 512000 / 100 * 80,
		},
		{
			name: "cgroup v2 without a limit",
			files: fstest.MapFS{
				"proc/meminfo":             file(meminfo),
				"sys/fs/cgroup/memory.max": file("max\n"),
			},
			want: machine / 100 * 80,
		},
		{
			name: "cgroup v1 limit above the machine",
			files: fstest.MapFS{
				"proc/meminfo": file(meminfo),
				"sys/fs/cgroup/memory/memory.limit_in_bytes": file("9223372036854771712\n"),
			},
			want: machine / 100 * 80,
		},
		{
			name:  "unreadable meminfo",
			files: fstest.MapFS{"proc/meminfo": file("MemTotal: lots kB\n")},
			want:  0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Default(tt.files))
		})
	}
}
