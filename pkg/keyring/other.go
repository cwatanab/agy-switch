//go:build !windows && !darwin

package keyring

import "github.com/ZhangNy301/agy-switch/pkg/config"

func platformStore(paths *config.Paths) TokenStore {
	return NewFileStore(paths)
}
