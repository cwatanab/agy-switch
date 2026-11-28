//go:build !windows

package shim

import (
	"fmt"
	"syscall"
)

func startRealAgyProcess(realAgy string, args []string, env []string) error {
	argv := append([]string{realAgy}, args...)
	if err := syscall.Exec(realAgy, argv, env); err != nil {
		return fmt.Errorf("exec %s: %w", realAgy, err)
	}
	return nil
}
