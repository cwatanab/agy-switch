//go:build linux

package bridge

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func CountAgyProcesses() (int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, err
	}

	count := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || pid == os.Getpid() {
			continue
		}

		commPath := filepath.Join("/proc", entry.Name(), "comm")
		comm, err := os.ReadFile(commPath)
		if err != nil {
			continue
		}
		name := strings.TrimSpace(string(comm))
		if name == "agy" || name == "agy.real" || name == "antigravity" {
			count++
		}
	}
	return count, nil
}
