package shim_test

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/proxy"
	"github.com/ZhangNy301/agy-switch/pkg/shim"
)

func TestBuildProxyEnv_ActiveHTTPProxy(t *testing.T) {
	tmpDir := t.TempDir()
	paths := &config.Paths{DataDir: tmpDir}
	store := proxy.NewStore(paths)

	cfg := proxy.Config{
		Name:     "corp-http",
		Server:   "proxy.corp.local",
		Port:     8080,
		Protocol: "http",
		Active:   true,
	}
	if err := store.Save(cfg); err != nil {
		t.Fatalf("save proxy: %v", err)
	}

	env, err := shim.BuildProxyEnv(paths, store)
	if err != nil {
		t.Fatalf("build proxy env: %v", err)
	}

	foundHTTP := false
	for _, e := range env {
		if e == "HTTP_PROXY=http://proxy.corp.local:8080" || e == "http_proxy=http://proxy.corp.local:8080" {
			foundHTTP = true
		}
	}
	if !foundHTTP {
		t.Errorf("expected HTTP_PROXY in env, got: %v", env)
	}
}

func TestShimPreservesAndRestoresExistingExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable and symlink fixtures")
	}
	for _, symlink := range []bool{false, true} {
		t.Run(map[bool]string{false: "regular", true: "symlink"}[symlink], func(t *testing.T) {
			dir := t.TempDir()
			paths := &config.Paths{DataDir: dir, BinDir: filepath.Join(dir, "bin")}
			if err := config.EnsureDirs(paths); err != nil {
				t.Fatal(err)
			}
			original := []byte("#!/bin/sh\necho original\n")
			path := filepath.Join(paths.BinDir, "agy")
			if symlink {
				target := filepath.Join(dir, "original")
				if err := os.WriteFile(target, original, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, original, 0755); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if err := shim.Install(paths, path); err != nil {
					t.Fatal(err)
				}
				backup, err := os.ReadFile(path + ".real")
				if err != nil || !bytes.Equal(backup, original) {
					t.Fatalf("original backup changed: %v", err)
				}
			}
			real, err := shim.FindRealAgy(paths)
			if err != nil || real != path+".real" {
				t.Fatalf("real agy not resolved to backup: %q, %v", real, err)
			}
			if err := shim.Uninstall(paths); err != nil {
				t.Fatal(err)
			}
			restored, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(restored, original) {
				t.Fatalf("original not restored: %v", err)
			}
			if symlink {
				if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
					t.Fatal("symlink was not restored")
				}
			}
		})
	}
}

func TestShimRefusesToOverwriteBackup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixture")
	}
	dir := t.TempDir()
	paths := &config.Paths{DataDir: dir, BinDir: dir}
	path := filepath.Join(dir, "agy")
	for _, file := range []string{path, path + ".real"} {
		if err := os.WriteFile(file, []byte("original"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := shim.Install(paths, path); err == nil {
		t.Fatal("overwrote an existing backup")
	}
	for _, file := range []string{path, path + ".real"} {
		data, err := os.ReadFile(file)
		if err != nil || string(data) != "original" {
			t.Fatalf("modified %s", file)
		}
	}
}

func TestInstallAndUninstallShim(t *testing.T) {
	tmpDir := t.TempDir()
	binDir := filepath.Join(tmpDir, "bin")
	paths := &config.Paths{
		DataDir: tmpDir,
		BinDir:  binDir,
	}

	realExe := filepath.Join(tmpDir, "real_agy")
	_ = os.WriteFile(realExe, []byte("#!/bin/sh\necho real"), 0755)

	if err := shim.Install(paths, realExe); err != nil {
		t.Fatalf("install shim failed: %v", err)
	}

	// Verify shim was created
	files, err := os.ReadDir(binDir)
	if err != nil || len(files) == 0 {
		t.Fatalf("expected shim in %s, got err: %v, files: %v", binDir, err, files)
	}

	// Verify Review Focus #5: Shim identifies itself and doesn't recurse
	shimPath := filepath.Join(binDir, files[0].Name())
	if !shim.IsShim(shimPath) {
		t.Errorf("expected %s to be recognized as shim", shimPath)
	}

	// Uninstall
	if err := shim.Uninstall(paths); err != nil {
		t.Fatalf("uninstall shim failed: %v", err)
	}
	filesAfter, _ := os.ReadDir(binDir)
	if len(filesAfter) != 0 {
		t.Errorf("expected binDir empty after uninstall, got %v", filesAfter)
	}
}

func TestBuildProxyEnv_RespectsExistingEnv(t *testing.T) {
	paths := &config.Paths{DataDir: t.TempDir()}
	store := proxy.NewStore(paths)
	if err := store.Save(proxy.Config{Name: "p1", Server: "proxy.local", Port: 8080, Protocol: "http", Active: true}); err != nil {
		t.Fatal(err)
	}
	// Clear lowercase variants before setting HTTP_PROXY (Windows env vars are case-insensitive).
	t.Setenv("http_proxy", "")
	t.Setenv("https_proxy", "")
	t.Setenv("all_proxy", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("ALL_PROXY", "")
	t.Setenv("HTTP_PROXY", "http://user-shell:3128")

	env, err := shim.BuildProxyEnv(paths, store)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range env {
		if strings.HasPrefix(entry, "HTTP_PROXY=") {
			t.Fatalf("existing HTTP_PROXY should not be overridden, got %s", entry)
		}
	}
	foundHTTPS := false
	for _, entry := range env {
		if entry == "HTTPS_PROXY=http://proxy.local:8080" {
			foundHTTPS = true
		}
	}
	if !foundHTTPS {
		t.Fatalf("expected HTTPS_PROXY to be filled, got %v", env)
	}
}

func TestBuildProxyEnv_SOCKSFallbackOnBridgeFailure(t *testing.T) {
	paths := &config.Paths{DataDir: t.TempDir(), BridgeLog: filepath.Join(t.TempDir(), "bridge.log")}
	store := proxy.NewStore(paths)
	if err := store.Save(proxy.Config{
		Name: "socks", Server: "127.0.0.1", Port: 1080, Protocol: "socks5h",
		Username: "alice", Password: "secret", Active: true,
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("ALL_PROXY", "")
	t.Setenv("http_proxy", "")
	t.Setenv("https_proxy", "")
	t.Setenv("all_proxy", "")

	orig := shim.SetEnsureBridgeForTest(func(*config.Paths, *proxy.Config) (int, string, error) {
		return 0, "", os.ErrPermission
	})
	defer shim.SetEnsureBridgeForTest(orig)

	env, err := shim.BuildProxyEnv(paths, store)
	if err != nil {
		t.Fatalf("expected fallback instead of error, got %v", err)
	}
	found := false
	for _, entry := range env {
		if entry == "HTTPS_PROXY=socks5h://alice:secret@127.0.0.1:1080" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected raw socks5h fallback URL, got %v", env)
	}
}

func TestRepairRestoresOverwrittenShim(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell fixture")
	}
	dir := t.TempDir()
	paths := &config.Paths{DataDir: dir, BinDir: filepath.Join(dir, "bin")}
	if err := config.EnsureDirs(paths); err != nil {
		t.Fatal(err)
	}
	store := proxy.NewStore(paths)
	if err := store.Save(proxy.Config{Name: "p1", Server: "127.0.0.1", Port: 8080, Protocol: "http", Active: true}); err != nil {
		t.Fatal(err)
	}
	shimPath := filepath.Join(paths.BinDir, "agy")
	realPath := shimPath + ".real"
	if err := os.WriteFile(shimPath, []byte("v1"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := shim.Install(paths, shimPath); err != nil {
		t.Fatal(err)
	}
	if err := store.SetShimPaths(shimPath, realPath); err != nil {
		t.Fatal(err)
	}
	// Simulate `agy update` overwriting shimPath with v2 binary
	if err := os.WriteFile(shimPath, []byte("v2"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := shim.Repair(paths, store); err != nil {
		t.Fatalf("Repair failed: %v", err)
	}
	if !shim.IsShim(shimPath) {
		t.Fatal("expected shimPath to be restored as shim")
	}
	gotReal, err := os.ReadFile(realPath)
	if err != nil || string(gotReal) != "v2" {
		t.Fatalf("expected updated binary at %s, got %q (%v)", realPath, gotReal, err)
	}
}
