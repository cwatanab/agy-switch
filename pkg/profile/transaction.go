package profile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ZhangNy301/agy-switch/pkg/filelock"
	"github.com/ZhangNy301/agy-switch/pkg/util"
)

type savedFile struct {
	Path   string `json:"path"`
	Data   []byte `json:"data"`
	Exists bool   `json:"exists"`
}

type recoveryState struct {
	LiveTracked bool        `json:"live_tracked"`
	LiveExists  bool        `json:"live_exists"`
	Live        []byte      `json:"live"`
	Files       []savedFile `json:"files"`
}

// Transaction serializes account mutations and restores their previous state on
// failure. An interrupted transaction is recovered before the next mutation.
// The callback must use the supplied manager rather than its original receiver.
func (m *Manager) Transaction(ctx context.Context, fn func(*Manager) error) (err error) {
	if m.locked {
		return fn(m)
	}
	release, err := filelock.Acquire(ctx, filepath.Join(m.paths.DataDir, "accounts.lock"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, release()) }()
	tx := *m
	tx.locked = true
	if err := tx.recoverPending(); err != nil {
		return fmt.Errorf("recover interrupted account update: %w", err)
	}
	tx.recovery = &recoveryState{}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.rollback()
			panic(p)
		}
	}()
	if err := fn(&tx); err != nil {
		return errors.Join(err, tx.rollback())
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, tx.rollback())
	}
	if err := removeIfExists(tx.recoveryPath()); err != nil {
		return errors.Join(fmt.Errorf("commit account update: %w", err), tx.rollback())
	}
	return nil
}

func (m *Manager) recoveryPath() string {
	return filepath.Join(m.paths.DataDir, ".account-recovery.json")
}

func (m *Manager) saveRecovery() error {
	data, err := json.Marshal(m.recovery)
	if err != nil {
		return err
	}
	return util.WriteFileAtomic(m.recoveryPath(), data, 0600)
}

func (m *Manager) rememberLive() error {
	if m.recovery.LiveTracked {
		return nil
	}
	data, err := m.store.ReadLive()
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	m.recovery.LiveTracked = true
	m.recovery.LiveExists = err == nil
	m.recovery.Live = data
	if err := m.saveRecovery(); err != nil {
		// No live write has happened yet, so rollback must not write this store.
		m.recovery.LiveTracked, m.recovery.LiveExists, m.recovery.Live = false, false, nil
		return err
	}
	return nil
}

func (m *Manager) rememberFile(path string) error {
	for _, file := range m.recovery.Files {
		if file.Path == path {
			return nil
		}
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	m.recovery.Files = append(m.recovery.Files, savedFile{Path: path, Data: data, Exists: err == nil})
	if err := m.saveRecovery(); err != nil {
		m.recovery.Files = m.recovery.Files[:len(m.recovery.Files)-1]
		return err
	}
	return nil
}

func (m *Manager) writeFile(path string, data []byte, perm os.FileMode) error {
	if err := m.rememberFile(path); err != nil {
		return err
	}
	return util.WriteFileAtomic(path, data, perm)
}

func (m *Manager) removeFile(path string) error {
	if err := m.rememberFile(path); err != nil {
		return err
	}
	return removeIfExists(path)
}

func (m *Manager) writeLive(data []byte) error {
	if err := m.rememberLive(); err != nil {
		return err
	}
	return m.store.WriteLive(data)
}

func (m *Manager) deleteLive() error {
	if err := m.rememberLive(); err != nil {
		return err
	}
	return m.store.DeleteLive()
}

func (m *Manager) recoverPending() error {
	data, err := os.ReadFile(m.recoveryPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var state recoveryState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("read recovery journal: %w", err)
	}
	m.recovery = &state
	return m.rollback()
}

func (m *Manager) rollback() error {
	if m.recovery == nil {
		return nil
	}
	var errs []error
	if m.recovery.LiveTracked {
		if m.recovery.LiveExists {
			errs = append(errs, m.store.WriteLive(m.recovery.Live))
		} else {
			errs = append(errs, m.store.DeleteLive())
		}
	}
	for i := len(m.recovery.Files) - 1; i >= 0; i-- {
		file := m.recovery.Files[i]
		if !m.ownsFile(file.Path) {
			errs = append(errs, fmt.Errorf("invalid path in recovery journal"))
			continue
		}
		if file.Exists {
			errs = append(errs, util.WriteFileAtomic(file.Path, file.Data, 0600))
		} else {
			errs = append(errs, removeIfExists(file.Path))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("account recovery incomplete; journal retained: %w", err)
	}
	return removeIfExists(m.recoveryPath())
}

func (m *Manager) ownsFile(path string) bool {
	return path == m.currentPath() || path == m.stashPath() ||
		(filepath.Dir(path) == m.paths.ProfilesDir && filepath.Ext(path) == ".json")
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
