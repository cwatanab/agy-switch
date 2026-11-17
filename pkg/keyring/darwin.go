//go:build darwin

package keyring

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/ZhangNy301/agy-switch/pkg/config"
)

const goKeyringPrefix = "go-keyring-base64:"

type DarwinKeyringStore struct {
	fallback *FileStore
}

func platformStore(paths *config.Paths) TokenStore {
	return &DarwinKeyringStore{
		fallback: NewFileStore(paths),
	}
}

func (d *DarwinKeyringStore) ReadLive() ([]byte, error) {
	cmd := exec.Command("/usr/bin/security", "find-generic-password", "-s", "gemini", "-a", "antigravity", "-w")
	out, err := cmd.Output()
	if err == nil {
		raw := strings.TrimSpace(string(out))
		if strings.HasPrefix(raw, goKeyringPrefix) {
			raw = strings.TrimPrefix(raw, goKeyringPrefix)
		}
		if decoded, bErr := base64.StdEncoding.DecodeString(raw); bErr == nil && len(decoded) > 0 {
			return decoded, nil
		}
		return []byte(raw), nil
	}
	return d.fallback.ReadLive()
}

func (d *DarwinKeyringStore) WriteLive(data []byte) error {
	encoded := goKeyringPrefix + base64.StdEncoding.EncodeToString(data)
	// Write via security interactive command (-i) so password never appears in process arguments
	cmd := exec.Command("/usr/bin/security", "-i")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return verifyWrite(d, data, err, d.fallback.WriteLive(data))
	}

	if err := cmd.Start(); err != nil {
		return verifyWrite(d, data, err, d.fallback.WriteLive(data))
	}

	secCmd := fmt.Sprintf("add-generic-password -U -s gemini -a antigravity -w %s\n", encoded)
	_, _ = io.WriteString(stdin, secCmd)
	_ = stdin.Close()

	return verifyWrite(d, data, cmd.Wait(), d.fallback.WriteLive(data))
}

func (d *DarwinKeyringStore) DeleteLive() error {
	cmd := exec.Command("/usr/bin/security", "delete-generic-password", "-s", "gemini", "-a", "antigravity")
	deleteErr := cmd.Run()
	fileErr := d.fallback.DeleteLive()
	if actual, err := d.ReadLive(); err == nil && len(actual) != 0 {
		return errors.Join(deleteErr, fileErr, errors.New("live credential remains after deletion"))
	}
	return fileErr
}
