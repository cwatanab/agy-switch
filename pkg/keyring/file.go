package keyring

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ZhangNy301/agy-switch/pkg/config"
)

type FileStore struct {
	paths *config.Paths
}

func NewFileStore(paths *config.Paths) *FileStore {
	return &FileStore{paths: paths}
}

func (f *FileStore) ReadLive() ([]byte, error) {
	data, err := os.ReadFile(f.paths.LiveToken)
	if err == nil {
		return data, nil
	}
	if os.IsNotExist(err) && f.paths.LegacyToken != "" {
		legacyData, lErr := os.ReadFile(f.paths.LegacyToken)
		if lErr == nil {
			return legacyData, nil
		}
	}
	return nil, err
}

func (f *FileStore) WriteLive(data []byte) error {
	dir := filepath.Dir(f.paths.LiveToken)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create token directory: %w", err)
	}

	tmpFile, err := os.CreateTemp(dir, "token-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp token file: %w", err)
	}
	tmpName := tmpFile.Name()
	defer os.Remove(tmpName)

	if err := tmpFile.Chmod(0600); err != nil {
		tmpFile.Close()
		return fmt.Errorf("chmod temp token: %w", err)
	}

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		return fmt.Errorf("write temp token: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		return fmt.Errorf("sync temp token: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp token: %w", err)
	}

	if err := os.Rename(tmpName, f.paths.LiveToken); err != nil {
		return fmt.Errorf("atomic rename token: %w", err)
	}
	return nil
}

func (f *FileStore) DeleteLive() error {
	var err1, err2 error
	if f.paths.LiveToken != "" {
		err1 = os.Remove(f.paths.LiveToken)
		if os.IsNotExist(err1) {
			err1 = nil
		}
	}
	if f.paths.LegacyToken != "" {
		err2 = os.Remove(f.paths.LegacyToken)
		if os.IsNotExist(err2) {
			err2 = nil
		}
	}
	if err1 != nil {
		return err1
	}
	return err2
}
