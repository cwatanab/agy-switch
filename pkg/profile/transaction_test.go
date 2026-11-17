package profile

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	"github.com/ZhangNy301/agy-switch/pkg/config"
)

type failingStore struct {
	data       []byte
	failWrites int
	deleteErr  error
}

func (s *failingStore) ReadLive() ([]byte, error) {
	if s.data == nil {
		return nil, os.ErrNotExist
	}
	return s.data, nil
}
func (s *failingStore) WriteLive(data []byte) error {
	if s.failWrites > 0 {
		s.failWrites--
		return syscall.ENOSPC
	}
	s.data = append([]byte(nil), data...)
	return nil
}
func (s *failingStore) DeleteLive() error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.data = nil
	return nil
}

func transactionFixture(t *testing.T) (*Manager, *failingStore, []byte) {
	t.Helper()
	dir := t.TempDir()
	paths := &config.Paths{DataDir: dir, ProfilesDir: filepath.Join(dir, "profiles")}
	old := []byte(`{"access_token":"old","refresh_token":"old-grant","email":"old@test.local"}`)
	store := &failingStore{data: old}
	manager := NewManager(paths, store)
	if err := manager.Snapshot("old"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Use("old"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Save("new", []byte(`{"access_token":"new","refresh_token":"new-grant","email":"new@test.local"}`)); err != nil {
		t.Fatal(err)
	}
	return manager, store, old
}

func TestUseRollsBackCredentialWriteFailure(t *testing.T) {
	manager, store, old := transactionFixture(t)
	store.failWrites = 1
	if err := manager.Use("new"); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("switch error=%v, want ENOSPC", err)
	}
	if !bytes.Equal(store.data, old) {
		t.Fatal("old live credential was not restored")
	}
	if name, err := manager.Current(); err != nil || name != "old" {
		t.Fatalf("current=%q, error=%v", name, err)
	}
}

func TestUseRejectsEmptyCredentialWithoutClearingLogin(t *testing.T) {
	manager, store, old := transactionFixture(t)
	if err := os.WriteFile(manager.profilePath("empty"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Use("empty"); err == nil {
		t.Fatal("empty credential was accepted")
	}
	if !bytes.Equal(store.data, old) {
		t.Fatal("invalid profile cleared the existing login")
	}
}

func TestJournalFailureDoesNotWriteLiveCredential(t *testing.T) {
	manager, store, old := transactionFixture(t)
	store.failWrites = 1
	err := manager.Transaction(context.Background(), func(tx *Manager) error {
		// An invalid journal destination simulates a persistence failure before
		// the first credential mutation, independently of host permissions.
		if err := os.Mkdir(tx.recoveryPath(), 0700); err != nil {
			return err
		}
		return tx.Use("new")
	})
	if err == nil {
		t.Fatal("journal failure was ignored")
	}
	if store.failWrites != 1 {
		t.Fatal("credential write attempted before journal was durable")
	}
	if !bytes.Equal(store.data, old) {
		t.Fatal("journal failure changed the existing credential")
	}
}

func TestDeleteKeepsProfileWhenNativeStoreFails(t *testing.T) {
	manager, store, old := transactionFixture(t)
	store.deleteErr = errors.New("native store denied deletion")
	if err := manager.Delete("old"); err == nil {
		t.Fatal("native store failure was ignored")
	}
	if _, err := os.Stat(manager.profilePath("old")); err != nil {
		t.Fatal("profile removed despite failed logout")
	}
	if !bytes.Equal(store.data, old) {
		t.Fatal("live credential changed after failed logout")
	}
}

func TestCanceledTransactionRestoresWholeLogin(t *testing.T) {
	manager, store, old := transactionFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	err := manager.Transaction(ctx, func(tx *Manager) error {
		if err := tx.Stash(); err != nil {
			return err
		}
		store.data = []byte(`{"access_token":"third","refresh_token":"third-grant","email":"third@test.local"}`)
		name, err := tx.SaveLive("third")
		if err != nil {
			return err
		}
		if err := tx.Use(name); err != nil {
			return err
		}
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("login error=%v, want canceled", err)
	}
	if !bytes.Equal(store.data, old) {
		t.Fatal("original credential lost after canceled login")
	}
	if _, err := os.Stat(manager.profilePath("third")); !os.IsNotExist(err) {
		t.Fatal("canceled profile was committed")
	}
	if name, err := manager.Current(); err != nil || name != "old" {
		t.Fatalf("current=%q, error=%v", name, err)
	}
}

func TestInterruptedUpdateIsRecovered(t *testing.T) {
	manager, store, old := transactionFixture(t)
	marker, err := os.ReadFile(manager.currentPath())
	if err != nil {
		t.Fatal(err)
	}
	manager.recovery = &recoveryState{LiveTracked: true, LiveExists: true, Live: old, Files: []savedFile{{Path: manager.currentPath(), Data: marker, Exists: true}}}
	if err := manager.saveRecovery(); err != nil {
		t.Fatal(err)
	}
	store.data = []byte(`{"access_token":"interrupted"}`)
	if err := os.WriteFile(manager.currentPath(), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	fresh := NewManager(manager.paths, store)
	if err := fresh.Transaction(context.Background(), func(*Manager) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(store.data, old) {
		t.Fatal("interrupted update was not recovered")
	}
	if name, err := fresh.Current(); err != nil || name != "old" {
		t.Fatalf("current=%q, error=%v", name, err)
	}
}

func TestConcurrentManagersSerializeUpdates(t *testing.T) {
	manager, _, _ := transactionFixture(t)
	// Different manager instances use the same process lock, as separate CLIs do.
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m := NewManager(manager.paths, manager.store)
			if err := m.Transaction(context.Background(), func(tx *Manager) error { return tx.Use("old") }); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if name, err := manager.Current(); err != nil || name != "old" {
		t.Fatalf("current=%q, error=%v", name, err)
	}
}
