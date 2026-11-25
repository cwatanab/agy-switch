package profile_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/profile"
)

type mockKeyring struct {
	data []byte
}

func TestManager_LoginPreservesAccounts(t *testing.T) {
	for _, alreadySaved := range []bool{false, true} {
		t.Run(fmt.Sprintf("saved=%v", alreadySaved), func(t *testing.T) {
			dir := t.TempDir()
			paths := &config.Paths{DataDir: dir, ProfilesDir: filepath.Join(dir, "profiles")}
			old := []byte(`{"token":{"access_token":"old","refresh_token":"rt-old"},"email":"shared@old.test"}`)
			store := &mockKeyring{data: old}
			mgr := profile.NewManager(paths, store)
			if alreadySaved {
				if err := mgr.Snapshot("shared"); err != nil {
					t.Fatal(err)
				}
			}
			if err := mgr.Stash(); err != nil {
				t.Fatal(err)
			}
			if store.data != nil {
				t.Fatal("live credential was not cleared for login")
			}
			store.data = []byte(`{"token":{"access_token":"new","refresh_token":"rt-new"},"email":"shared@new.test"}`)
			name, err := mgr.SaveLive("")
			if err != nil {
				t.Fatal(err)
			}
			if name != "shared-2" {
				t.Fatalf("collision should produce shared-2, got %q", name)
			}
			mgr.DropStash()
			got, err := os.ReadFile(filepath.Join(paths.ProfilesDir, "shared.json"))
			if err != nil || !bytes.Equal(got, old) {
				t.Fatalf("old account lost: %v", err)
			}
			// Repeated logins with the same grant refresh its existing profile.
			store.data = []byte(`{"token":{"access_token":"refreshed","refresh_token":"rt-new"},"email":"shared@new.test"}`)
			again, err := mgr.SaveLive("")
			if err != nil || again != name {
				t.Fatalf("same grant created a duplicate: %q, %v", again, err)
			}
			profiles, err := mgr.List()
			if err != nil || len(profiles) != 2 {
				t.Fatalf("expected two preserved accounts: %d, %v", len(profiles), err)
			}
			if err := mgr.Use("shared"); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(store.data, old) {
				t.Fatal("old account could not be restored")
			}
		})
	}
}

func (m *mockKeyring) ReadLive() ([]byte, error) {
	if m.data == nil {
		return nil, os.ErrNotExist
	}
	return m.data, nil
}

func (m *mockKeyring) WriteLive(d []byte) error {
	m.data = d
	return nil
}

func (m *mockKeyring) DeleteLive() error {
	m.data = nil
	return nil
}

func TestManager_SnapshotAndUse(t *testing.T) {
	tmpDir := t.TempDir()
	paths := &config.Paths{
		DataDir:     tmpDir,
		ProfilesDir: filepath.Join(tmpDir, "profiles"),
		LiveToken:   filepath.Join(tmpDir, "live-token"),
	}
	if err := config.EnsureDirs(paths); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}

	liveJSON := []byte(`{"access_token":"token-123","email":"dev@example.com"}`)
	mockStore := &mockKeyring{data: liveJSON}

	mgr := profile.NewManager(paths, mockStore)

	// Snapshot
	if err := mgr.Snapshot("dev"); err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}

	// Verify profile file permissions (0600)
	profPath := filepath.Join(paths.ProfilesDir, "dev.json")
	info, err := os.Stat(profPath)
	if err != nil {
		t.Fatalf("stat profile file: %v", err)
	}
	if perm := info.Mode().Perm(); runtime.GOOS != "windows" && perm != 0600 {
		t.Errorf("expected 0600 perm, got %o", perm)
	}

	// List
	profiles, err := mgr.List()
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(profiles) != 1 || profiles[0].Name != "dev" {
		t.Fatalf("unexpected profiles: %+v", profiles)
	}

	// Change live data and Use to restore
	mockStore.data = []byte(`{"access_token":"other"}`)
	if err := mgr.Use("dev"); err != nil {
		t.Fatalf("use failed: %v", err)
	}

	tok, err := profile.ParseTokenJSON(mockStore.data)
	if err != nil {
		t.Fatalf("parse restored token: %v", err)
	}
	if tok.AccessToken != "token-123" {
		t.Errorf("expected restored token-123, got %s", tok.AccessToken)
	}

	// Rename
	if err := mgr.Rename("dev", "work"); err != nil {
		t.Fatalf("rename failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(paths.ProfilesDir, "work.json")); err != nil {
		t.Errorf("expected work.json to exist")
	}

	// Delete
	if err := mgr.Delete("work"); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	listAfterDelete, err := mgr.List()
	if err != nil || len(listAfterDelete) != 0 {
		t.Errorf("expected empty list after delete, got %v", listAfterDelete)
	}
}

func TestManager_CurrentAndReassert(t *testing.T) {
	tmpDir := t.TempDir()
	paths := &config.Paths{
		DataDir:     tmpDir,
		ProfilesDir: filepath.Join(tmpDir, "profiles"),
		LiveToken:   filepath.Join(tmpDir, "live-token"),
	}
	_ = config.EnsureDirs(paths)

	p1JSON := []byte(`{"token":{"access_token":"at-1","refresh_token":"rt-1"},"email":"user1@example.com"}`)
	mockStore := &mockKeyring{data: p1JSON}
	mgr := profile.NewManager(paths, mockStore)

	if err := mgr.Snapshot("user1"); err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if err := mgr.Use("user1"); err != nil {
		t.Fatalf("use failed: %v", err)
	}

	cur, err := mgr.Current()
	if err != nil || cur != "user1" {
		t.Fatalf("expected current user1, got %s (err %v)", cur, err)
	}

	// Clobber live token (simulating another process)
	mockStore.data = []byte(`{"token":{"access_token":"at-clobber","refresh_token":"rt-clobber"}}`)
	if err := mgr.ReassertCurrent(); err != nil {
		t.Fatalf("reassert failed: %v", err)
	}

	tok, err := profile.ParseTokenJSON(mockStore.data)
	if err != nil {
		t.Fatalf("parse reasserted token: %v", err)
	}
	if tok.RefreshToken != "rt-1" {
		t.Errorf("expected reasserted rt-1, got %s", tok.RefreshToken)
	}

	// Rename
	if err := mgr.Rename("user1", "user1-renamed"); err != nil {
		t.Fatalf("rename failed: %v", err)
	}
	cur, _ = mgr.Current()
	if cur != "user1-renamed" {
		t.Errorf("expected current to update to user1-renamed, got %s", cur)
	}

	// Delete
	if err := mgr.Delete("user1-renamed"); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	cur, _ = mgr.Current()
	if cur != "" {
		t.Errorf("expected current to be empty after delete, got %s", cur)
	}
	if mockStore.data != nil {
		t.Errorf("expected live credential to be deleted when active profile deleted")
	}
}

func TestManager_AdoptLiveIfOrphan(t *testing.T) {
	tmpDir := t.TempDir()
	paths := &config.Paths{
		DataDir:     tmpDir,
		ProfilesDir: filepath.Join(tmpDir, "profiles"),
		LiveToken:   filepath.Join(tmpDir, "live-token"),
	}
	_ = config.EnsureDirs(paths)

	liveJSON := []byte(`{"token":{"access_token":"live-at","refresh_token":"live-rt"},"id_token_email":"orphan@example.com"}`)
	mockStore := &mockKeyring{data: liveJSON}
	mgr := profile.NewManager(paths, mockStore)

	adopted, err := mgr.AdoptLiveIfOrphan()
	if err != nil {
		t.Fatalf("adopt failed: %v", err)
	}
	if adopted != "orphan" {
		t.Errorf("expected adopted name 'orphan', got %s", adopted)
	}

	profiles, err := mgr.List()
	if err != nil || len(profiles) != 1 {
		t.Fatalf("expected 1 adopted profile, got %d (err %v)", len(profiles), err)
	}
	if profiles[0].Name != "orphan" || profiles[0].Email != "orphan@example.com" {
		t.Errorf("unexpected adopted profile: %+v", profiles[0])
	}

	cur, _ := mgr.Current()
	if cur != "orphan" {
		t.Errorf("expected current to be orphan, got %s", cur)
	}

	// Second call should do nothing since profiles already exist
	secondAdopt, err := mgr.AdoptLiveIfOrphan()
	if err != nil || secondAdopt != "" {
		t.Errorf("expected second call to return empty, got %s (err %v)", secondAdopt, err)
	}
}

func TestManager_MetaActiveNameAndClobber(t *testing.T) {
	dir := t.TempDir()
	paths := &config.Paths{
		DataDir:     dir,
		ProfilesDir: filepath.Join(dir, "profiles"),
		LiveToken:   filepath.Join(dir, "live-token"),
	}
	_ = config.EnsureDirs(paths)

	p1 := []byte(`{"token":{"access_token":"at-1","refresh_token":"rt-1"},"email":"one@example.com"}`)
	p2 := []byte(`{"token":{"access_token":"at-2","refresh_token":"rt-2"},"email":"two@example.com"}`)
	store := &mockKeyring{data: p1}
	mgr := profile.NewManager(paths, store)

	if err := mgr.Snapshot("one"); err != nil {
		t.Fatal(err)
	}
	store.data = p2
	if err := mgr.Snapshot("two"); err != nil {
		t.Fatal(err)
	}

	// Without current file, ActiveName falls back to matching live refresh token ("two")
	if got := mgr.ActiveName(); got != "two" {
		t.Fatalf("ActiveName() = %q, want two", got)
	}

	if err := mgr.Use("one"); err != nil {
		t.Fatal(err)
	}
	list, err := mgr.List()
	if err != nil {
		t.Fatal(err)
	}
	var oneProf profile.Profile
	for _, p := range list {
		if p.Name == "one" {
			oneProf = p
		}
	}
	if oneProf.ActivatedAt.IsZero() {
		t.Fatal("expected ActivatedAt to be recorded on Use")
	}

	// Clobber live token with two's refresh token
	store.data = p2
	if clobbered, cur := mgr.LiveWasClobbered(); !clobbered || cur != "one" {
		t.Fatalf("LiveWasClobbered() = (%v, %q), want (true, one)", clobbered, cur)
	}

	if err := mgr.Rename("one", "one-renamed"); err != nil {
		t.Fatal(err)
	}
	list, _ = mgr.List()
	found := false
	for _, p := range list {
		if p.Name == "one-renamed" && !p.ActivatedAt.IsZero() {
			found = true
		}
	}
	if !found {
		t.Fatal("expected ActivatedAt to follow Rename")
	}
}
