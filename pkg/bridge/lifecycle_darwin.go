//go:build darwin

package bridge

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func CountAgyProcesses() (int, error) {
	cmd := exec.Command("pgrep", "-x", "agy|agy[.]real|antigravity")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return 0, nil
		}
		return 0, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	count := 0
	for _, l := range lines {
		if pid, err := strconv.Atoi(strings.TrimSpace(l)); err == nil && pid != os.Getpid() {
			count++
		}
	}
	return count, nil
}
