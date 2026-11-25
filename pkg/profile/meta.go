package profile

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/filelock"
	"github.com/ZhangNy301/agy-switch/pkg/util"
)

type ProfileMeta struct {
	ActivatedAt int64 `json:"activated_at,omitempty"`
}

func (m *Manager) metaPath() string {
	return filepath.Join(m.paths.DataDir, "meta.json")
}

func (m *Manager) readMetaUnlocked() (map[string]ProfileMeta, error) {
	meta := make(map[string]ProfileMeta)
	if m.paths == nil || m.paths.DataDir == "" {
		return meta, nil
	}
	data, err := os.ReadFile(m.metaPath())
	if os.IsNotExist(err) {
		return meta, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return make(map[string]ProfileMeta), nil
	}
	if meta == nil {
		meta = make(map[string]ProfileMeta)
	}
	return meta, nil
}

func (m *Manager) LoadMeta() (map[string]ProfileMeta, error) {
	if m.paths == nil || m.paths.DataDir == "" {
		return map[string]ProfileMeta{}, nil
	}
	return m.readMetaUnlocked()
}

func (m *Manager) updateMeta(fn func(map[string]ProfileMeta)) (err error) {
	if m.paths == nil || m.paths.DataDir == "" {
		return nil
	}
	if err := config.EnsureDirs(m.paths); err != nil {
		return err
	}
	release, err := filelock.Acquire(context.Background(), filepath.Join(m.paths.DataDir, "meta.lock"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, release()) }()
	meta, err := m.readMetaUnlocked()
	if err != nil {
		return err
	}
	fn(meta)
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return util.WriteFileAtomic(m.metaPath(), data, 0600)
}

func (m *Manager) RecordActivated(name string, at time.Time) error {
	if name == "" {
		return nil
	}
	return m.updateMeta(func(meta map[string]ProfileMeta) {
		entry := meta[name]
		entry.ActivatedAt = at.Unix()
		meta[name] = entry
	})
}

func (m *Manager) renameMeta(oldName, newName string) {
	_ = m.updateMeta(func(meta map[string]ProfileMeta) {
		if entry, ok := meta[oldName]; ok {
			delete(meta, oldName)
			meta[newName] = entry
		}
	})
}

func (m *Manager) deleteMeta(name string) {
	_ = m.updateMeta(func(meta map[string]ProfileMeta) {
		delete(meta, name)
	})
}
