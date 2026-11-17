package keyring_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/keyring"
)

func TestFileStore_ReadWriteDelete(t *testing.T) {
	tmpDir := t.TempDir()
	livePath := filepath.Join(tmpDir, "token")
	paths := &config.Paths{
		LiveToken: livePath,
	}

	store := keyring.NewFileStore(paths)

	// Read on empty should return ErrNotExist
	_, err := store.ReadLive()
	if !os.IsNotExist(err) && err == nil {
		t.Errorf("expected ErrNotExist, got %v", err)
	}

	data := []byte(`{"access_token":"abc"}`)
	if err := store.WriteLive(data); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	// Verify permissions
	info, err := os.Stat(livePath)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	if perm := info.Mode().Perm(); runtime.GOOS != "windows" && perm != 0600 {
		t.Errorf("expected 0600, got %o", perm)
	}

	read, err := store.ReadLive()
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if string(read) != string(data) {
		t.Errorf("expected %s, got %s", data, read)
	}

	if err := store.DeleteLive(); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if _, err := os.Stat(livePath); !os.IsNotExist(err) {
		t.Errorf("file still exists after delete")
	}
}
