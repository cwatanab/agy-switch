//go:build !windows

package quota

import (
	"bytes"
	"context"
	"os/exec"
)

func execUsageCommand(ctx context.Context, agyBin string, args []string, env []string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, agyBin, args...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}
