package config_test

import (
	"path/filepath"
	"testing"

	"github.com/ZhangNy301/agy-switch/pkg/config"
)

func TestResolvePaths_ContainsDataAndProfiles(t *testing.T) {
	p, err := config.ResolvePaths()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.DataDir == "" || p.ProfilesDir == "" || p.LiveToken == "" {
		t.Errorf("expected non-empty paths, got: %+v", p)
	}
	if p.ProfilesDir != filepath.Join(p.DataDir, "profiles") {
		t.Errorf("expected profiles dir inside data dir, got %s", p.ProfilesDir)
	}
}

func TestEnsureDirs(t *testing.T) {
	tmpDir := t.TempDir()
	p := &config.Paths{
		DataDir:     filepath.Join(tmpDir, "data"),
		ProfilesDir: filepath.Join(tmpDir, "data", "profiles"),
		BinDir:      filepath.Join(tmpDir, "bin"),
	}
	if err := config.EnsureDirs(p); err != nil {
		t.Fatalf("failed to ensure dirs: %v", err)
	}
}
