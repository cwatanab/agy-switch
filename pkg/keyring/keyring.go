package keyring

import "github.com/ZhangNy301/agy-switch/pkg/config"

type TokenStore interface {
	ReadLive() ([]byte, error)
	WriteLive(data []byte) error
	DeleteLive() error
}

func DefaultStore(paths *config.Paths) TokenStore {
	return platformStore(paths)
}
