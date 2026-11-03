// Package memlimit works out a default soft memory limit for the process.
package memlimit

import (
	"io/fs"
	"strconv"
	"strings"
)

const (
	percent      = 80
	bytesPerKB   = 1024
	memInfoPath  = "proc/meminfo"
	memInfoTotal = "MemTotal:"
)

// containerLimitPaths hold a container's memory limit, in cgroup v2 and v1 layouts.
var containerLimitPaths = []string{
	"sys/fs/cgroup/memory.max",
	"sys/fs/cgroup/memory/memory.limit_in_bytes",
}

// Default returns about 80% of the memory available to this process: the machine's total or the container's
// limit, whichever is lower. It reads from fsys, rooted at /, and returns 0 if it finds neither, as on systems
// other than Linux.
func Default(fsys fs.FS) int64 {
	available := machineBytes(fsys)
	for _, path := range containerLimitPaths {
		if limit := containerBytes(fsys, path); limit > 0 && (available == 0 || limit < available) {
			available = limit
		}
	}
	return available / 100 * percent
}

func machineBytes(fsys fs.FS) int64 {
	data, err := fs.ReadFile(fsys, memInfoPath)
	if err != nil {
		return 0
	}
	for line := range strings.Lines(string(data)) {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] != memInfoTotal || fields[2] != "kB" {
			continue
		}
		kb, err := strconv.Atoi(fields[1])
		if err != nil {
			return 0
		}
		return int64(kb) * bytesPerKB
	}
	return 0
}

// containerBytes returns 0 if the file is missing or says "max", meaning no limit.
func containerBytes(fsys fs.FS, path string) int64 {
	data, err := fs.ReadFile(fsys, path)
	if err != nil {
		return 0
	}
	limit, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return int64(limit)
}
