package main

import (
	"bytes"
	"encoding/json"
	"github.com/ZhangNy301/agy-switch/pkg/keyring"
	"github.com/ZhangNy301/agy-switch/pkg/quota"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ZhangNy301/agy-switch/pkg/cli"
	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/profile"
)

type mockKeyringStore struct {
	data []byte
}

func (m *mockKeyringStore) ReadLive() ([]byte, error) {
	return m.data, nil
}

func (m *mockKeyringStore) WriteLive(d []byte) error {
	m.data = d
	return nil
}

func (m *mockKeyringStore) DeleteLive() error {
	m.data = nil
	return nil
}

func TestCLI_VersionCommand(t *testing.T) {
	paths := &config.Paths{DataDir: t.TempDir()}
	rootCmd := cli.BuildRootCmd(paths)

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"version"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute version failed: %v", err)
	}

	if !strings.Contains(buf.String(), "agy-switch") {
		t.Errorf("expected agy-switch in version output, got: %s", buf.String())
	}
}

func TestCLI_LsCommand(t *testing.T) {
	tmpDir := t.TempDir()
	paths := &config.Paths{
		DataDir:     tmpDir,
		ProfilesDir: filepath.Join(tmpDir, "profiles"),
		LiveToken:   filepath.Join(tmpDir, "live"),
	}
	_ = config.EnsureDirs(paths)

	mockStore := &mockKeyringStore{
		data: []byte(`{"access_token":"fake","email":"dev@test.local"}`),
	}
	mgr := profile.NewManager(paths, mockStore)
	if err := mgr.Snapshot("dev"); err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}

	cache, err := json.Marshal(map[string]quota.QuotaInfo{"dev@test.local": {Tier: "Pro", UpdatedAt: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "quota_cache.json"), cache, 0600); err != nil {
		t.Fatal(err)
	}
	rootCmd := cli.NewCommand(paths, cli.Dependencies{
		TokenStore:      keyring.NewFileStore(paths),
		DisableNetCheck: true,
	})

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"ls"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute ls failed: %v", err)
	}

	if !strings.Contains(buf.String(), "dev") {
		t.Errorf("expected dev in ls output, got: %s", buf.String())
	}
}
